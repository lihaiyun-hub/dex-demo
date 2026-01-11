import { defineConfig } from "hardhat/config";
import hardhatToolboxViem from "@nomicfoundation/hardhat-toolbox-viem";
import path from "node:path";
import { fileURLToPath } from "node:url";

export default defineConfig({
  plugins: [hardhatToolboxViem],
  solidity: {
    version: "0.8.24",
    path: path.join(
      path.dirname(fileURLToPath(import.meta.url)),
      "node_modules",
      "solc",
      "soljson.js",
    ),
  },
});
