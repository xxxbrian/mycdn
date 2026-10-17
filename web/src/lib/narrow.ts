export function enumValue<const T extends string>(
  value: string,
  allowed: readonly T[],
  field: string,
): T {
  const choice = allowed.find((item) => item === value);
  if (choice === undefined) throw new Error(`Unsupported ${field}: ${value}`);
  return choice;
}

export function required<T>(value: T | undefined, field: string): T {
  if (value === undefined) throw new Error(`${field} is required.`);
  return value;
}
