// Guards the client against the vendored contract: an operation or a model
// the OpenAPI document declares must exist here, so a refresh of testdata/
// that adds one fails loudly instead of shipping an SDK that quietly lacks it.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

import { SmklogClient, VERSION } from '../index.js';

const openapi = JSON.parse(readFileSync(new URL('../../testdata/openapi.json', import.meta.url), 'utf8'));
const declarations = readFileSync(new URL('../index.d.ts', import.meta.url), 'utf8');
const pkg = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'));

const HTTP_METHODS = ['get', 'post', 'put', 'patch', 'delete'];

test('every operation in openapi.json has a method on SmklogClient', () => {
  const operations = [];
  for (const [path, item] of Object.entries(openapi.paths)) {
    for (const method of HTTP_METHODS) {
      if (item[method]) operations.push({ path, method, operationId: item[method].operationId });
    }
  }
  assert.ok(operations.length >= 4, `expected at least the four known operations, found ${operations.length}`);
  for (const { path, method, operationId } of operations) {
    assert.equal(typeof operationId, 'string', `${method.toUpperCase()} ${path} has no operationId`);
    assert.equal(
      typeof SmklogClient.prototype[operationId],
      'function',
      `${method.toUpperCase()} ${path} is declared as ${operationId} but the client has no such method`,
    );
  }
});

test('every component schema is declared in index.d.ts', () => {
  for (const name of Object.keys(openapi.components.schemas)) {
    const declared = new RegExp(`^export (interface|class) ${name}\\b`, 'm').test(declarations);
    assert.ok(declared, `schema ${name} has no exported type in index.d.ts`);
  }
});

test('the default base URL is the production server from the contract', () => {
  assert.equal(new SmklogClient().baseUrl, openapi.servers[0].url);
});

test('VERSION matches package.json', () => {
  assert.equal(VERSION, pkg.version);
  assert.equal(pkg.name, 'smklog');
});
