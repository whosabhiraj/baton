#!/usr/bin/env node
import { main } from '../cli/baton.ts';

process.exitCode = await main(process.argv.slice(2));
