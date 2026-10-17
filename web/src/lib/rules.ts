import type {
  ActionCapability,
  Capabilities,
  Condition,
  FieldCapability,
  Rule,
  Stage,
  Step,
} from '../types';
import { uid } from './format';

export const MAX_TREE_DEPTH = 8;
export const MAX_SIBLINGS = 32;
export function fieldsAtStage(
  capabilities: Capabilities | undefined,
  stage: Stage,
): FieldCapability[] {
  return capabilities?.fields.filter((field) => field.stages.includes(stage)) || [];
}
export function actionsAtStage(
  capabilities: Capabilities | undefined,
  stage: Stage,
): ActionCapability[] {
  return capabilities?.actions.filter((action) => action.stages.includes(stage)) || [];
}
export function defaultCondition(fields: FieldCapability[]): Condition {
  const field = fields[0];
  return {
    type: 'match',
    field: field?.name || 'host',
    op: field?.operators[0] || 'eq',
    values: [''],
  };
}
export function copySteps(steps: Step[]): Step[] {
  return steps.map((step) =>
    step.type === 'action'
      ? { ...structuredClone(step), id: uid() }
      : {
          ...step,
          id: uid(),
          branches: step.branches.map((branch) => ({
            ...structuredClone(branch),
            id: uid(),
            steps: copySteps(branch.steps),
          })),
        },
  );
}
export function copyRule(rule: Rule): Rule {
  return { ...rule, id: uid(), name: `${rule.name} copy`, steps: copySteps(rule.steps) };
}
export function stepCount(steps: Step[]): number {
  return steps.reduce(
    (count, step) =>
      count +
      1 +
      (step.type === 'if'
        ? step.branches.reduce((sum, branch) => sum + stepCount(branch.steps), 0)
        : 0),
    0,
  );
}
export function pretty(value: string) {
  return value.replace(/[_.]/g, ' ').replace(/\b\w/g, (value) => value.toUpperCase());
}
