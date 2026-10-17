import { describe, expect, it } from 'vitest';
import { copyRule, stepCount } from './rules';
import type { Rule } from '../types';

describe('owned rule AST', () => {
  const rule: Rule = {
    id: 'rule',
    name: 'Images',
    enabled: true,
    stage: 'request',
    steps: [
      {
        id: 'if',
        type: 'if',
        branches: [
          {
            id: 'branch',
            condition: { type: 'all', children: [] },
            steps: [
              {
                id: 'action',
                type: 'action',
                action: { type: 'cache', cache: { prefetch: false, ttl_seconds: 0 } },
              },
            ],
          },
          { id: 'else', condition: null, steps: [] },
        ],
      },
    ],
  };
  it('duplicates the entire ordered tree with fresh stable IDs and independent patches', () => {
    const copy = copyRule(rule);
    expect(copy.id).not.toBe(rule.id);
    expect(copy.steps[0].id).not.toBe('if');
    const step = copy.steps[0];
    if (step.type !== 'if') throw new Error('wrong step');
    expect(step.branches[0].id).not.toBe('branch');
    expect(step.branches[1].condition).toBeNull();
    const action = step.branches[0].steps[0];
    if (action.type !== 'action') throw new Error('wrong step');
    expect(action.action.cache).toEqual({ prefetch: false, ttl_seconds: 0 });
    action.action.cache!.ttl_seconds = 60;
    expect(JSON.stringify(rule)).toContain('"ttl_seconds":0');
    expect(stepCount(copy.steps)).toBe(2);
  });
});
