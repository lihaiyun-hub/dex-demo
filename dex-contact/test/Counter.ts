import assert from "node:assert/strict";
import { test } from "node:test";
import { network } from "hardhat";

test("Counter increments", async () => {
  const { viem } = await network.connect();

  const counter = await viem.deployContract("Counter");

  assert.equal(await counter.read.x(), 0n);
  await counter.write.inc();
  assert.equal(await counter.read.x(), 1n);
});

