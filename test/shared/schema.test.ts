import { expect, test } from 'bun:test';
import { categoryFor } from '../../src/shared/schema';

test('categoryFor maps Claude tools', () => {
  expect(categoryFor('claude','Read')).toBe('read'); expect(categoryFor('claude','MultiEdit')).toBe('edit');
  expect(categoryFor('claude','NotebookEdit')).toBe('edit'); expect(categoryFor('claude','Write')).toBe('write');
  expect(categoryFor('claude','Bash')).toBe('command'); expect(categoryFor('claude','Glob')).toBe('search');
  expect(categoryFor('claude','Task')).toBe('agent'); expect(categoryFor('claude','mcp__x__y')).toBe('other');
});
test('categoryFor maps Codex log and daemon names', () => {
  for (const n of ['apply_patch','FileChange','fileChange']) expect(categoryFor('codex',n)).toBe('edit');
  for (const n of ['shell','exec_command','local_shell','CommandExecution','commandExecution']) expect(categoryFor('codex',n)).toBe('command');
  for (const n of ['spawn_agent','CollabAgentToolCall','collabAgentToolCall']) expect(categoryFor('codex',n)).toBe('agent');
  expect(categoryFor('codex','webSearch')).toBe('search');
  for (const n of ['exec','send_message','McpToolCall','wait_agent']) expect(categoryFor('codex',n)).toBe('other');
});
