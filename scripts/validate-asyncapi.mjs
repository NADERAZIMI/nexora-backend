#!/usr/bin/env node
// Validates AsyncAPI documents with the official AsyncAPI parser.
import { readFileSync, globSync } from "node:fs";
import { Parser } from "@asyncapi/parser";

const patterns = process.argv.slice(2);
const files = patterns.flatMap((pattern) => globSync(pattern));

if (files.length === 0) {
  console.error("No AsyncAPI files matched:", patterns.join(", "));
  process.exit(1);
}

const parser = new Parser();
let failed = false;

for (const file of files) {
  const { diagnostics } = await parser.parse(readFileSync(file, "utf8"), {
    source: file,
  });

  const errors = diagnostics.filter((diagnostic) => diagnostic.severity === 0);
  for (const diagnostic of diagnostics) {
    const label = diagnostic.severity === 0 ? "error" : "warning";
    console.error(`${file}: ${label}: ${diagnostic.message}`);
  }

  if (errors.length > 0) {
    failed = true;
  } else {
    console.log(`${file}: valid`);
  }
}

process.exit(failed ? 1 : 0);
