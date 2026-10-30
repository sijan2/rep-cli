#!/usr/bin/env node
// The interaction fixture is driven entirely by the built rep subprocess.
import {execFileSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
const runner = fileURLToPath(new URL('./agent_first_runner.py', import.meta.url));
execFileSync('python3', [runner, '--scenario', 'interaction', ...process.argv.slice(2)], {stdio: 'inherit'});
