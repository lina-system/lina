// SPDX-License-Identifier: Apache-2.0
import * as sdk from '@anthropic-ai/claude-agent-sdk';
import * as z from 'zod/v4';
import { serve } from './adapter.ts';
import { sdkRuntime } from './runtime.ts';

await serve(process.stdin, process.stdout, sdkRuntime(sdk, z));
