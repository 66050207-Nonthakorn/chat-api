// Run from the repository root with the pinned Bruno CLI's npm environment.
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const bin = process.env.PATH.split(path.delimiter).find(dir => fs.existsSync(path.join(dir, 'bru.cmd')) || fs.existsSync(path.join(dir, 'bru')));
assert(bin, 'Run through npm exec --package=@usebruno/cli@4.1.0');
const lang = require(path.join(path.dirname(bin), '@usebruno/lang'));
const root = 'bruno/cloud-chat-local';
let count = 0;
for (const name of fs.readdirSync(root).filter(name => name.endsWith('.bru'))) {
  const source = fs.readFileSync(path.join(root, name), 'utf8');
  assert(lang.bruToJsonV2(source), name);
  for (const match of source.matchAll(/(?:tests|script:(?:pre-request|post-response)) \{([\s\S]*?)^\}/gm)) {
    new vm.Script(match[1], {filename: name});
  }
  for (const match of source.matchAll(/@file\(([^)]+)\)/g)) {
    assert(fs.statSync(path.join(root, match[1])).size > 0, name + ': missing fixture');
  }
  count++;
}
assert(lang.bruToEnvJsonV2(fs.readFileSync(path.join(root, 'environments/local.bru'), 'utf8')));
const fixture = fs.readFileSync(path.join(root, 'fixtures/image.png'));
assert.equal(fixture.subarray(1,4).toString(), 'PNG');
assert.equal(fixture.readUInt32BE(16), 8);
assert.equal(fixture.readUInt32BE(20), 8);
console.log(`Validated ${count} Bruno requests, environment, JavaScript blocks, and PNG fixture paths.`);
