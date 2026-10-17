import { Copy, CornerDownRight, GitBranch, Plus, Trash2, Zap } from 'lucide-react';
import type { ActionCapability, Branch, FieldCapability, Site, Step } from '../types';
import { copySteps, defaultCondition, MAX_SIBLINGS, MAX_TREE_DEPTH } from '../lib/rules';
import { uid } from '../lib/format';
import { Alert, Badge, Button, IconButton, SortableList } from './ui';
import ActionEditor, { newAction } from './ActionEditor';
import ConditionEditor from './ConditionEditor';

interface EditorContext {
  fields: FieldCapability[];
  actions: ActionCapability[];
  sites: Site[];
  depth: number;
}
interface Props {
  steps: Step[];
  onChange: (steps: Step[]) => void;
  fields: FieldCapability[];
  actions: ActionCapability[];
  sites: Site[];
  depth?: number;
}

function BranchChain({
  value,
  onChange,
  fields,
  actions,
  sites,
  depth,
}: EditorContext & { value: Branch[]; onChange: (branches: Branch[]) => void }) {
  const conditions = value.filter((branch) => branch.condition !== null);
  const otherwise = value.find((branch) => branch.condition === null);
  function update(next: Branch) {
    onChange(value.map((branch) => (branch.id === next.id ? next : branch)));
  }
  function renderBranch(branch: Branch, index: number) {
    return (
      <div className="branch" key={branch.id}>
        <div className="branch-heading">
          <Badge tone={branch.condition === null ? '' : 'accent'}>
            {branch.condition === null ? 'ELSE' : index === 0 ? 'IF' : 'ELSE IF'}
          </Badge>
          <span className="field-hint">
            {branch.condition === null
              ? 'When no preceding branch matched'
              : 'Only the first matching branch runs'}
          </span>
          <IconButton
            label={`Remove ${branch.condition === null ? 'ELSE' : `branch ${index + 1}`}`}
            disabled={branch.condition !== null && conditions.length <= 1}
            onClick={() => onChange(value.filter((item) => item.id !== branch.id))}
          >
            <Trash2 size={12} />
          </IconButton>
        </div>
        {branch.condition !== null && (
          <ConditionEditor
            condition={branch.condition}
            onChange={(condition) => update({ ...branch, condition })}
            fields={fields}
          />
        )}
        <div className="branch-steps">
          <div className="then-label">
            <CornerDownRight size={12} />
            THEN
          </div>
          {depth >= MAX_TREE_DEPTH ? (
            <Alert tone="warning">
              Editor depth limit reached. Existing nested steps are preserved. Simplify this tree
              before adding more nesting.
            </Alert>
          ) : (
            <StepEditor
              steps={branch.steps}
              onChange={(steps) => update({ ...branch, steps })}
              fields={fields}
              actions={actions}
              sites={sites}
              depth={depth + 1}
            />
          )}
        </div>
      </div>
    );
  }
  return (
    <div className="branch-chain">
      <SortableList
        items={conditions}
        label="branch"
        onChange={(next) => onChange([...next, ...(otherwise ? [otherwise] : [])])}
        render={renderBranch}
      />
      {otherwise && <div className="else-branch">{renderBranch(otherwise, conditions.length)}</div>}
      <div className="branch-add">
        <Button
          variant="ghost"
          disabled={value.length >= MAX_SIBLINGS || !fields.length}
          onClick={() =>
            onChange([
              ...conditions,
              { id: uid(), condition: defaultCondition(fields), steps: [] },
              ...(otherwise ? [otherwise] : []),
            ])
          }
        >
          <Plus size={12} />
          ELSE IF
        </Button>
        <Button
          variant="ghost"
          disabled={!!otherwise || value.length >= MAX_SIBLINGS}
          onClick={() => onChange([...conditions, { id: uid(), condition: null, steps: [] }])}
        >
          <Plus size={12} />
          ELSE
        </Button>
      </div>
    </div>
  );
}
export default function StepEditor({ steps, onChange, fields, actions, sites, depth = 0 }: Props) {
  function setStep(index: number, step: Step) {
    onChange(steps.map((current, i) => (i === index ? step : current)));
  }
  function add(kind: 'action' | 'if') {
    const preferred = actions.find((action) => action.type === 'cache') || actions[0];
    onChange([
      ...steps,
      kind === 'action'
        ? { id: uid(), type: 'action', action: newAction(preferred?.type || '') }
        : {
            id: uid(),
            type: 'if',
            branches: [{ id: uid(), condition: defaultCondition(fields), steps: [] }],
          },
    ]);
  }
  return (
    <div className={`step-list depth-${depth}`}>
      <SortableList
        items={steps}
        onChange={onChange}
        label="step"
        render={(step, index) => (
          <div className={`step-node ${step.type}`}>
            <div className="step-heading">
              <span>
                {step.type === 'if' ? <GitBranch size={13} /> : <Zap size={13} />}
                <strong>{step.type === 'if' ? 'BRANCH CHAIN' : 'ACTION'}</strong>
                <span className="mono muted">{String(index + 1).padStart(2, '0')}</span>
              </span>
              <div className="inline-actions">
                <IconButton
                  label="Duplicate step"
                  disabled={steps.length >= MAX_SIBLINGS}
                  onClick={() => {
                    const next = [...steps];
                    next.splice(index + 1, 0, copySteps([step])[0]);
                    onChange(next);
                  }}
                >
                  <Copy size={12} />
                </IconButton>
                <IconButton
                  label="Delete step"
                  onClick={() => onChange(steps.filter((_, i) => i !== index))}
                >
                  <Trash2 size={12} />
                </IconButton>
              </div>
            </div>
            {step.type === 'action' ? (
              <ActionEditor
                action={step.action}
                onChange={(action) => setStep(index, { ...step, action })}
                available={actions}
                sites={sites}
              />
            ) : (
              <BranchChain
                value={step.branches}
                onChange={(branches) => setStep(index, { ...step, branches })}
                fields={fields}
                actions={actions}
                sites={sites}
                depth={depth}
              />
            )}
          </div>
        )}
      />
      {!steps.length && (
        <div className="step-empty">No steps. Add an action or a conditional branch.</div>
      )}
      <div className="step-add">
        <Button
          variant="ghost"
          disabled={steps.length >= MAX_SIBLINGS || !actions.length}
          onClick={() => add('action')}
        >
          <Plus size={12} />
          <Zap size={12} />
          Action
        </Button>
        <Button
          variant="ghost"
          disabled={steps.length >= MAX_SIBLINGS || depth >= MAX_TREE_DEPTH || !fields.length}
          onClick={() => add('if')}
        >
          <Plus size={12} />
          <GitBranch size={12} />
          IF branch
        </Button>
      </div>
    </div>
  );
}
