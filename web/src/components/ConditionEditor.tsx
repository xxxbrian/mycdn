import { Braces, Plus, Trash2 } from 'lucide-react';
import { useRef } from 'react';
import type { Condition, FieldCapability } from '../types';
import { defaultCondition, MAX_SIBLINGS, MAX_TREE_DEPTH, pretty } from '../lib/rules';
import { uid } from '../lib/format';
import { Alert, Button, Field, IconButton, Input, Select, SortableList } from './ui';

const operatorLabels: Record<string, string> = {
  eq: 'equals',
  ne: 'does not equal',
  in: 'is one of',
  not_in: 'is not one of',
  prefix: 'starts with',
  suffix: 'ends with',
  contains: 'contains',
  glob: 'matches glob',
  regex: 'matches regex',
  exists: 'exists',
  not_exists: 'does not exist',
  cidr: 'is in CIDR',
  gt: 'greater than',
  gte: 'at least',
  lt: 'less than',
  lte: 'at most',
};
interface Props {
  condition: Condition;
  onChange: (condition: Condition) => void;
  fields: FieldCapability[];
  depth?: number;
  onRemove?: () => void;
}
export default function ConditionEditor({
  condition,
  onChange,
  fields,
  depth = 0,
  onRemove,
}: Props) {
  // Conditions have no wire IDs. Local keys follow object identity, including
  // immutable edits, rather than treating an array position as node identity.
  const identities = useRef(new WeakMap<Condition, string>());
  function keyFor(value: Condition) {
    let key = identities.current.get(value);
    if (!key) {
      key = uid();
      identities.current.set(value, key);
    }
    return key;
  }
  function changeType(type: Condition['type']) {
    if (type === condition.type) return;
    if (type === 'match') onChange(defaultCondition(fields));
    else if (type === 'not') onChange({ type, children: [condition] });
    else
      onChange({ type, children: condition.type === 'match' ? [condition] : condition.children });
  }
  const match = condition.type === 'match' ? condition : undefined;
  const capability = fields.find((field) => field.name === match?.field);
  const operators = capability?.operators || (match ? [match.op] : []);
  const header = (
    <div className="condition-heading">
      <span className="condition-label">
        <Braces size={12} />
        {match ? 'MATCH' : 'GROUP'}
      </span>
      <Select
        aria-label="Condition logic"
        value={condition.type}
        onChange={(event) => changeType(event.target.value as Condition['type'])}
      >
        <option value="all" disabled={depth >= MAX_TREE_DEPTH && condition.type === 'match'}>
          ALL · AND
        </option>
        <option value="any" disabled={depth >= MAX_TREE_DEPTH && condition.type === 'match'}>
          ANY · OR
        </option>
        <option value="not" disabled={depth >= MAX_TREE_DEPTH}>
          NOT
        </option>
        <option value="match">Condition</option>
      </Select>
      {onRemove && (
        <IconButton label="Remove condition" onClick={onRemove}>
          <Trash2 size={12} />
        </IconButton>
      )}
    </div>
  );
  if (condition.type === 'match') {
    const noValue = ['exists', 'not_exists'].includes(condition.op);
    return (
      <div className="condition-node condition-match">
        {header}
        {!capability && (
          <Alert tone="warning">
            This field is unavailable at the selected stage. Choose a compatible field or change the
            rule stage; its current condition is preserved.
          </Alert>
        )}
        <div className="condition-fields">
          <Field label="Field">
            <Select
              value={condition.field}
              onChange={(event) => {
                const field = fields.find((field) => field.name === event.target.value);
                onChange({
                  type: 'match',
                  field: event.target.value,
                  op: field?.operators[0] || 'eq',
                  ...(field?.key ? { key: '' } : {}),
                  values: [''],
                });
              }}
            >
              {!capability && (
                <option value={condition.field}>{pretty(condition.field)} (not in catalog)</option>
              )}
              {fields.map((field) => (
                <option key={field.name} value={field.name}>
                  {field.label || pretty(field.name)}
                </option>
              ))}
            </Select>
          </Field>
          {(capability?.key || condition.key !== undefined) && (
            <Field label="Header / query / cookie key">
              <Input
                required
                value={condition.key || ''}
                placeholder="e.g. X-Region"
                onChange={(event) => onChange({ ...condition, key: event.target.value })}
              />
            </Field>
          )}
          <Field label="Operator">
            <Select
              value={condition.op}
              onChange={(event) =>
                onChange({
                  ...condition,
                  op: event.target.value,
                  values: ['exists', 'not_exists'].includes(event.target.value)
                    ? []
                    : ['in', 'not_in', 'cidr'].includes(event.target.value)
                      ? condition.values?.length
                        ? condition.values
                        : ['']
                      : [condition.values?.[0] || ''],
                })
              }
            >
              {!operators.includes(condition.op) && (
                <option value={condition.op}>
                  {pretty(condition.op)} (unavailable for this field)
                </option>
              )}
              {operators.map((op) => (
                <option key={op} value={op}>
                  {operatorLabels[op] || pretty(op)}
                </option>
              ))}
            </Select>
          </Field>
          {!noValue && (
            <div className="match-values">
              <span className="field-label">Value{condition.values?.length !== 1 ? 's' : ''}</span>
              {(condition.values?.length ? condition.values : ['']).map((value, index) => (
                <div className="value-row" key={index}>
                  <Input
                    value={value}
                    aria-label={`Condition value ${index + 1}`}
                    placeholder={condition.op === 'cidr' ? '192.0.2.0/24' : 'Match value'}
                    onChange={(event) => {
                      const values = condition.values?.length ? [...condition.values] : [''];
                      values[index] = event.target.value;
                      onChange({ ...condition, values });
                    }}
                  />
                  {condition.values?.length > 1 && (
                    <IconButton
                      label={`Remove value ${index + 1}`}
                      onClick={() =>
                        onChange({
                          ...condition,
                          values: condition.values.filter((_, i) => i !== index),
                        })
                      }
                    >
                      <Trash2 size={12} />
                    </IconButton>
                  )}
                </div>
              ))}
              {['in', 'not_in', 'cidr'].includes(condition.op) && (
                <Button
                  variant="ghost"
                  disabled={(condition.values?.length || 0) >= MAX_SIBLINGS}
                  onClick={() =>
                    onChange({ ...condition, values: [...(condition.values || []), ''] })
                  }
                >
                  <Plus size={11} />
                  Value
                </Button>
              )}
              <span className="field-hint">
                Empty string is a value. Use exists / does not exist to test presence.
              </span>
            </div>
          )}
        </div>
      </div>
    );
  }
  // Go omits an empty children slice; normalize it at this external boundary.
  const children = condition.children || [];
  return (
    <div className="condition-node condition-group">
      {header}
      <div className="condition-children">
        {!children.length && (
          <div className="match-all">
            {condition.type === 'all'
              ? 'All requests · explicit match-all'
              : condition.type === 'any'
                ? 'No requests · empty OR group'
                : 'NOT requires one condition'}
          </div>
        )}
        {condition.type === 'not' ? (
          children[0] && (
            <ConditionEditor
              condition={children[0]}
              onChange={(child) => onChange({ ...condition, children: [child] })}
              fields={fields}
              depth={depth + 1}
            />
          )
        ) : (
          <SortableList
            label="condition"
            items={children.map((child) => ({ id: keyFor(child), child }))}
            onChange={(items) =>
              onChange({ ...condition, children: items.map((item) => item.child) })
            }
            render={(item, index) => (
              <ConditionEditor
                condition={item.child}
                fields={fields}
                depth={depth + 1}
                onChange={(child) => {
                  identities.current.set(child, item.id);
                  onChange({
                    ...condition,
                    children: children.map((current, i) => (i === index ? child : current)),
                  });
                }}
                onRemove={() =>
                  onChange({ ...condition, children: children.filter((_, i) => i !== index) })
                }
              />
            )}
          />
        )}
      </div>
      {(condition.type !== 'not' || !children.length) && (
        <div className="condition-add">
          <Button
            variant="ghost"
            disabled={children.length >= MAX_SIBLINGS || depth >= MAX_TREE_DEPTH || !fields.length}
            onClick={() =>
              onChange({ ...condition, children: [...children, defaultCondition(fields)] })
            }
          >
            <Plus size={12} />
            Condition
          </Button>
          {condition.type !== 'not' && (
            <Button
              variant="ghost"
              disabled={
                children.length >= MAX_SIBLINGS || depth + 1 >= MAX_TREE_DEPTH || !fields.length
              }
              onClick={() =>
                onChange({
                  ...condition,
                  children: [...children, { type: 'any', children: [defaultCondition(fields)] }],
                })
              }
            >
              <Plus size={12} />
              OR group
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
