import type { ExplainInput } from '../types';

export function withResponseStatus(input: ExplainInput, status: number | undefined): ExplainInput {
  return {
    ...input,
    response:
      status === undefined
        ? undefined
        : {
            ...input.response,
            status,
            cache_result: input.response?.cache_result || 'MISS',
          },
  };
}
