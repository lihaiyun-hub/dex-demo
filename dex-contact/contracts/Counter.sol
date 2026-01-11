pragma solidity ^0.8.24;

contract Counter {
    uint256 public x;

    function inc() public {
        x += 1;
    }
}
