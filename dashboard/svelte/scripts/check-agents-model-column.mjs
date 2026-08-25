import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const agentsPage = readFileSync(new URL('../src/routes/agents/+page.svelte', import.meta.url), 'utf8');
const types = readFileSync(new URL('../src/lib/types.ts', import.meta.url), 'utf8');

assert.match(types, /last_model:\s*string;/, 'Agent must type last_model');
assert.match(types, /models_used:\s*string\[\];/, 'Agent must type models_used');
assert.match(agentsPage, /label:\s*'Model'/, 'Agents table must include the Model column');
assert.match(agentsPage, /font-mono/, 'Model values must use the monospace font');
assert.match(agentsPage, /<details[\s>]/, 'Multiple models must use a keyboard-accessible disclosure');
assert.match(agentsPage, /<summary[\s>]/, 'The model disclosure must have an accessible summary');
assert.match(agentsPage, /modelsByAgent\[row\.name\]/, 'The model disclosure must render the complete model history');

console.log('Agents model column smoke check passed.');
