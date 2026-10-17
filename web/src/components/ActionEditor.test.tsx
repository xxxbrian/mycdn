import { describe, expect, it } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import ActionEditor, { newAction } from './ActionEditor';
import type { ActionCapability } from '../types';

const actions: ActionCapability[] = [
  ['identity', 'identity'],
  ['limits', 'limits'],
  ['cache', 'cache'],
  ['cache_key', 'cache_key'],
  ['origin', 'origin'],
  ['path', 'path'],
  ['query', 'query'],
  ['cookie', 'cookie'],
  ['request_header', 'header'],
  ['authority', 'authority'],
  ['upstream', 'upstream'],
  ['source_header', 'header'],
  ['response_header', 'header'],
  ['browser_ttl', 'browser_ttl_seconds'],
  ['cors', 'cors'],
  ['reply', 'reply'],
  ['redirect', 'redirect'],
].map(([type, parameter]) => ({ type, parameter, stages: ['request'] }));
describe('graphical capability forms', () => {
  it('creates exactly one typed parameter and renders a form for every core action', () => {
    for (const capability of actions) {
      const action = newAction(capability.type);
      expect(Object.keys(action).sort()).toEqual(['type', capability.parameter].sort());
      const html = renderToStaticMarkup(
        <ActionEditor action={action} onChange={() => {}} available={actions} sites={[]} />,
      );
      expect(html).not.toContain('does not support');
      expect(html).toContain('class="field-label"');
    }
  });
});
