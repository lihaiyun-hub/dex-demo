package scanner

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// ERC20 ABI 定义（仅包含我们需要的方法）
var erc20ABI = `[
	{
		"constant": true,
		"inputs": [],
		"name": "symbol",
		"outputs": [{"name": "", "type": "string"}],
		"type": "function"
	},
	{
		"constant": true,
		"inputs": [],
		"name": "name",
		"outputs": [{"name": "", "type": "string"}],
		"type": "function"
	},
	{
		"constant": true,
		"inputs": [],
		"name": "decimals",
		"outputs": [{"name": "", "type": "uint8"}],
		"type": "function"
	}
]`

// ensureToken 确保代币记录存在于数据库中，从 ERC20 合约读取 symbol、name 和 decimals
func (s *Scanner) ensureToken(addr common.Address) {
	// 先检查数据库中是否已存在
	var exists bool
	err := s.DB.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM tokens WHERE address = $1)
	`, addr.Hex()).Scan(&exists)
	if err != nil {
		log.Printf("Error checking token existence: %v", err)
		return
	}
	if exists {
		// 代币已存在，跳过
		return
	}

	// 从 ERC20 合约读取信息
	symbol := "UNK"
	name := "Unknown"
	decimals := int64(18)

	parsedABI, err := abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		log.Printf("Error parsing ERC20 ABI: %v", err)
		// 使用默认值插入
		s.insertToken(addr, symbol, name, decimals)
		return
	}

	ctx := context.Background()

	// 调用 symbol()
	if symbolMethod, ok := parsedABI.Methods["symbol"]; ok {
		data, err := parsedABI.Pack("symbol")
		if err == nil {
			result, err := s.Client.CallContract(ctx, ethereum.CallMsg{
				To:   &addr,
				Data: data,
			}, nil)
			if err == nil {
				unpacked, err := symbolMethod.Outputs.Unpack(result)
				if err == nil && len(unpacked) > 0 {
					if s, ok := unpacked[0].(string); ok {
						symbol = s
					}
				}
			}
		}
	}

	// 调用 name()
	if nameMethod, ok := parsedABI.Methods["name"]; ok {
		data, err := parsedABI.Pack("name")
		if err == nil {
			result, err := s.Client.CallContract(ctx, ethereum.CallMsg{
				To:   &addr,
				Data: data,
			}, nil)
			if err == nil {
				unpacked, err := nameMethod.Outputs.Unpack(result)
				if err == nil && len(unpacked) > 0 {
					if n, ok := unpacked[0].(string); ok {
						name = n
					}
				}
			}
		}
	}

	// 调用 decimals()
	if decimalsMethod, ok := parsedABI.Methods["decimals"]; ok {
		data, err := parsedABI.Pack("decimals")
		if err == nil {
			result, err := s.Client.CallContract(ctx, ethereum.CallMsg{
				To:   &addr,
				Data: data,
			}, nil)
			if err == nil {
				unpacked, err := decimalsMethod.Outputs.Unpack(result)
				if err == nil && len(unpacked) > 0 {
					// decimals 返回 uint8
					switch v := unpacked[0].(type) {
					case uint8:
						decimals = int64(v)
					case uint16:
						decimals = int64(v)
					case uint32:
						decimals = int64(v)
					case uint64:
						decimals = int64(v)
					case *big.Int:
						decimals = v.Int64()
					default:
						log.Printf("Unexpected decimals type for token %s: %T", addr.Hex(), v)
					}
				}
			}
		}
	}

	// 插入数据库
	s.insertToken(addr, symbol, name, decimals)
}

// insertToken 将代币信息插入数据库
func (s *Scanner) insertToken(addr common.Address, symbol, name string, decimals int64) {
	_, err := s.DB.Exec(`
		INSERT INTO tokens (address, symbol, name, decimals)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (address) DO NOTHING
	`, addr.Hex(), symbol, name, decimals)
	if err != nil {
		log.Printf("Error inserting token: %v", err)
	} else {
		log.Printf("Inserted token: %s (symbol=%s, name=%s, decimals=%d)", addr.Hex(), symbol, name, decimals)
	}
}

// ensurePoolExists 尝试从数据库加载池子信息，如果不存在则创建一个基本记录
func (s *Scanner) ensurePoolExists(poolAddr common.Address) {
	// Check if pool exists in DB
	var exists bool
	err := s.DB.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM pools WHERE address = $1)
	`, poolAddr.Hex()).Scan(&exists)

	if err != nil {
		log.Printf("Error checking pool existence: %v", err)
		return
	}

	if exists {
		// Pool exists in DB, add to cache
		s.Pools[poolAddr] = true
	} else {
		// Pool doesn't exist in DB, we can't create it without pool creation event
		// But we can still add it to cache to process events
		s.Pools[poolAddr] = true
	}
}

// updateTicksFromMint 从 Mint 事件更新 ticks 表的流动性
// 注意：在这个简化实现中，所有流动性都在池子的 tickLower 到 tickUpper 之间
// 所以我们需要更新这两个边界 tick 的流动性
func (s *Scanner) updateTicksFromMint(poolAddr common.Address, liquidity *big.Int) {
	// 查询池子的 tick_lower 和 tick_upper
	var tickLower, tickUpper int
	err := s.DB.QueryRow(`
		SELECT tick_lower, tick_upper FROM pools WHERE address = $1
	`, poolAddr.Hex()).Scan(&tickLower, &tickUpper)
	if err != nil {
		log.Printf("Error querying pool ticks for update: %v", err)
		return
	}

	// 更新 tick_lower 的流动性
	// liquidity_gross: 总流动性（累加）
	// liquidity_net: 净流动性变化（向上为正，这里在 tickLower 处，价格向上移动时流动性增加）
	_, err = s.DB.Exec(`
		INSERT INTO ticks (
			pool_address, tick_index, liquidity_gross, liquidity_net,
			fee_growth_outside0_x128, fee_growth_outside1_x128
		) VALUES ($1, $2, $3, $4, 0, 0)
		ON CONFLICT (pool_address, tick_index) DO UPDATE SET
			liquidity_gross = ticks.liquidity_gross + $3,
			liquidity_net = ticks.liquidity_net + $4,
			updated_at = NOW()
	`, poolAddr.Hex(), tickLower, liquidity.String(), liquidity.String())
	if err != nil {
		log.Printf("Error updating tick_lower: %v", err)
	}

	// 更新 tick_upper 的流动性
	// 在 tickUpper 处，价格向上移动时流动性减少（所以 liquidity_net 为负）
	liquidityNeg := new(big.Int).Neg(liquidity)
	_, err = s.DB.Exec(`
		INSERT INTO ticks (
			pool_address, tick_index, liquidity_gross, liquidity_net,
			fee_growth_outside0_x128, fee_growth_outside1_x128
		) VALUES ($1, $2, $3, $4, 0, 0)
		ON CONFLICT (pool_address, tick_index) DO UPDATE SET
			liquidity_gross = ticks.liquidity_gross + $3,
			liquidity_net = ticks.liquidity_net + $4,
			updated_at = NOW()
	`, poolAddr.Hex(), tickUpper, liquidity.String(), liquidityNeg.String())
	if err != nil {
		log.Printf("Error updating tick_upper: %v", err)
	}
}

// updateTicksFromBurn 从 Burn 事件更新 ticks 表的流动性
func (s *Scanner) updateTicksFromBurn(poolAddr common.Address, liquidity *big.Int) {
	// 查询池子的 tick_lower 和 tick_upper
	var tickLower, tickUpper int
	err := s.DB.QueryRow(`
		SELECT tick_lower, tick_upper FROM pools WHERE address = $1
	`, poolAddr.Hex()).Scan(&tickLower, &tickUpper)
	if err != nil {
		log.Printf("Error querying pool ticks for update: %v", err)
		return
	}

	// 更新 tick_lower 的流动性（减少）
	_, err = s.DB.Exec(`
		UPDATE ticks SET
			liquidity_gross = GREATEST(0, liquidity_gross - $1),
			liquidity_net = liquidity_net - $1,
			updated_at = NOW()
		WHERE pool_address = $2 AND tick_index = $3
	`, liquidity.String(), poolAddr.Hex(), tickLower)
	if err != nil {
		log.Printf("Error updating tick_lower on burn: %v", err)
	}

	// 更新 tick_upper 的流动性（减少，liquidity_net 增加，因为负值减少）
	_, err = s.DB.Exec(`
		UPDATE ticks SET
			liquidity_gross = GREATEST(0, liquidity_gross - $1),
			liquidity_net = liquidity_net + $1,
			updated_at = NOW()
		WHERE pool_address = $2 AND tick_index = $3
	`, liquidity.String(), poolAddr.Hex(), tickUpper)
	if err != nil {
		log.Printf("Error updating tick_upper on burn: %v", err)
	}
}

// getPoolLiquidity 查询 Pool 合约的当前流动性
// 注意：这需要 Pool 合约有 liquidity() 方法，如果查询失败则返回错误
// 目前未实现，因为我们可以从 Swap 事件中获取流动性，或者使用累加/累减方式
func (s *Scanner) getPoolLiquidity(poolAddr common.Address, blockNumber uint64) (*big.Int, error) {
	// 未实现，返回错误
	return nil, fmt.Errorf("not implemented")
}
