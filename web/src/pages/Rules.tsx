import { Copy, GitBranch, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useConfig } from '../config';
import {
  actionsAtStage,
  copyRule,
  defaultCondition,
  fieldsAtStage,
  pretty,
  stepCount,
} from '../lib/rules';
import { uid } from '../lib/format';
import type { Rule, Stage } from '../types';
import { stages } from '../types';
import {
  Alert,
  Badge,
  Button,
  CheckField,
  ConfirmButton,
  Empty,
  Field,
  Input,
  PageHeading,
  Panel,
  Select,
  SortableList,
} from '../components/ui';
import StepEditor from '../components/StepEditor';

export default function Rules() {
  const { draft, change, capabilities, capabilitiesError } = useConfig();
  const [selected, setSelected] = useState<string>();
  const [stage, setStage] = useState('all');
  if (!draft) return null;
  const selectedRule = draft.rules.find((rule) => rule.id === selected);
  const visible =
    stage === 'all' ? draft.rules : draft.rules.filter((rule) => rule.stage === stage);
  function setRule(rule: Rule) {
    change((config) => ({
      ...config,
      rules: config.rules.map((item) => (item.id === rule.id ? rule : item)),
    }));
  }
  function create() {
    const ruleStage: Stage = stage === 'all' ? 'request' : (stage as Stage);
    const rule: Rule = {
      id: uid(),
      name: 'New rule',
      stage: ruleStage,
      enabled: true,
      steps: [
        {
          id: uid(),
          type: 'if',
          branches: [
            {
              id: uid(),
              condition: defaultCondition(fieldsAtStage(capabilities, ruleStage)),
              steps: [],
            },
          ],
        },
      ],
    };
    change((config) => ({ ...config, rules: [...config.rules, rule] }));
    setSelected(rule.id);
  }
  return (
    <>
      <PageHeading
        title="Graphical rules"
        eyebrow="POLICY / ORDERED EXECUTION"
        description="Typed conditions and actions. No scripts, no canvas, no hidden priority."
        action={
          <Button variant="primary" onClick={create} disabled={!capabilities}>
            <Plus size={14} />
            Create rule
          </Button>
        }
      />
      {capabilitiesError && (
        <Alert>
          The capability catalog could not be loaded: {capabilitiesError}. Existing rules remain
          visible; new rules and actions are disabled.
        </Alert>
      )}
      <div className="rule-semantics">
        <span>
          <Badge>01</Badge>INGRESS
        </span>
        <span>→</span>
        <span>
          <Badge>02</Badge>REQUEST
        </span>
        <span>→</span>
        <span className="muted">CACHE / ORIGIN</span>
        <span>→</span>
        <span>
          <Badge>03</Badge>ORIGIN RESPONSE
        </span>
        <span>→</span>
        <span>
          <Badge>04</Badge>DELIVERY
        </span>
      </div>
      {selectedRule ? (
        <>
          <div className="editor-toolbar">
            <Button onClick={() => setSelected(undefined)}>← All rules</Button>
            <span className="muted text-small">
              Changes stay in the shared draft until Save configuration.
            </span>
          </div>
          <Panel
            title="Rule configuration"
            action={
              <div className="inline-actions">
                <Button
                  onClick={() => {
                    const copy = copyRule(selectedRule);
                    change((config) => ({ ...config, rules: [...config.rules, copy] }));
                    setSelected(copy.id);
                  }}
                >
                  <Copy size={13} />
                  Duplicate
                </Button>
                <ConfirmButton
                  confirm="Delete rule"
                  onConfirm={() => {
                    change((config) => ({
                      ...config,
                      rules: config.rules.filter((rule) => rule.id !== selectedRule.id),
                    }));
                    setSelected(undefined);
                  }}
                >
                  <Trash2 size={13} />
                  Delete
                </ConfirmButton>
              </div>
            }
          >
            <div className="form-grid two">
              <Field label="Rule name">
                <Input
                  required
                  value={selectedRule.name}
                  onChange={(event) => setRule({ ...selectedRule, name: event.target.value })}
                />
              </Field>
              <Field
                label="Matching stage"
                hint="Changing the stage can invalidate existing fields or actions. The server validates the whole tree."
              >
                <Select
                  value={selectedRule.stage}
                  onChange={(event) =>
                    setRule({ ...selectedRule, stage: event.target.value as Stage })
                  }
                >
                  {stages.map((stage) => (
                    <option key={stage} value={stage}>
                      {pretty(stage)}
                    </option>
                  ))}
                </Select>
              </Field>
              <CheckField
                label="Rule enabled"
                checked={selectedRule.enabled}
                onChange={(enabled) => setRule({ ...selectedRule, enabled })}
              />
            </div>
          </Panel>
          <Panel
            title="Execution steps"
            subtitle="Array order is priority. Independent IF blocks all run; a branch chain chooses only its first match."
            action={<Badge>{stepCount(selectedRule.steps)} STEPS</Badge>}
          >
            <StepEditor
              steps={selectedRule.steps}
              onChange={(steps) => setRule({ ...selectedRule, steps })}
              fields={fieldsAtStage(capabilities, selectedRule.stage)}
              actions={actionsAtStage(capabilities, selectedRule.stage)}
              sites={draft.sites}
            />
          </Panel>
          <Alert tone="info">
            Conditions read a stage-entry snapshot. Actions change the output plan, not the inputs
            of later conditions in the same stage. Reply and redirect are terminal but still pass
            through delivery rules.
          </Alert>
        </>
      ) : (
        <>
          <div className="list-toolbar">
            <div>
              <span className="eyebrow">GLOBAL RULE ORDER</span>
              <p className="muted text-small">
                Drag within this list or use the up/down buttons. Order is the only priority.
              </p>
            </div>
            <Field label="Stage">
              <Select value={stage} onChange={(event) => setStage(event.target.value)}>
                <option value="all">All stages</option>
                {stages.map((stage) => (
                  <option key={stage} value={stage}>
                    {pretty(stage)}
                  </option>
                ))}
              </Select>
            </Field>
          </div>
          {!visible.length ? (
            <Panel>
              <Empty
                title={
                  draft.rules.length ? 'No rules in this stage' : 'Explicit policy starts here'
                }
                description="Create a rule, match requests, and add only the fields you want to override."
                icon={<GitBranch size={28} />}
              />
              <Button variant="primary" onClick={create} disabled={!capabilities}>
                <Plus size={13} />
                Create rule
              </Button>
            </Panel>
          ) : (
            <SortableList
              label="rule"
              items={visible}
              onChange={(rules) =>
                change((config) => {
                  if (stage === 'all') return { ...config, rules };
                  let index = 0;
                  return {
                    ...config,
                    rules: config.rules.map((rule) =>
                      rule.stage === stage ? rules[index++] : rule,
                    ),
                  };
                })
              }
              render={(rule, index) => (
                <div className={`rule-row ${!rule.enabled ? 'disabled-rule' : ''}`}>
                  <span className="rule-index mono">{String(index + 1).padStart(2, '0')}</span>
                  <CheckField
                    label={`Enable ${rule.name}`}
                    checked={rule.enabled}
                    onChange={(enabled) => setRule({ ...rule, enabled })}
                  />
                  <button type="button" className="rule-name" onClick={() => setSelected(rule.id)}>
                    <strong>{rule.name}</strong>
                    <span className="mono muted text-small">
                      {stepCount(rule.steps)} steps · {rule.id.slice(0, 8)}
                    </span>
                  </button>
                  <Badge>{pretty(rule.stage)}</Badge>
                  <div className="inline-actions">
                    <Button onClick={() => setSelected(rule.id)}>Edit</Button>
                    <Button
                      aria-label={`Duplicate ${rule.name}`}
                      onClick={() => {
                        const copy = copyRule(rule);
                        change((config) => {
                          const rules = [...config.rules];
                          rules.splice(
                            config.rules.findIndex((item) => item.id === rule.id) + 1,
                            0,
                            copy,
                          );
                          return { ...config, rules };
                        });
                      }}
                    >
                      <Copy size={13} />
                    </Button>
                    <ConfirmButton
                      confirm="Delete rule"
                      onConfirm={() =>
                        change((config) => ({
                          ...config,
                          rules: config.rules.filter((item) => item.id !== rule.id),
                        }))
                      }
                    >
                      <Trash2 size={13} />
                    </ConfirmButton>
                  </div>
                </div>
              )}
            />
          )}
        </>
      )}
    </>
  );
}
