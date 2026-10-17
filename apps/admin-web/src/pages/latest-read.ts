export type ReadState<T> =
  | {key: string; phase: 'loading'}
  | {key: string; phase: 'ready'; value: T}
  | {key: string; phase: 'error'; error: unknown};

// A cancelled transport can still resolve (for example a cached response).
// Only the latest generation may publish, even if AbortSignal is ignored.
export function createLatestRead<T>(publish: (state: ReadState<T>) => void) {
  let generation = 0;
  let controller: AbortController | undefined;
  const cancel = () => { generation++; controller?.abort(); controller = undefined; };
  return {
    cancel,
    async start(key: string, read: (signal: AbortSignal) => Promise<T>) {
      cancel();
      const current = generation;
      const request = new AbortController();
      controller = request;
      publish({key, phase: 'loading'});
      try {
        const value = await read(request.signal);
        if (current === generation && !request.signal.aborted) publish({key, phase: 'ready', value});
      } catch (error) {
        if (current === generation && !request.signal.aborted) publish({key, phase: 'error', error});
      }
    },
  };
}

// Hide old content during the render before the next effect starts, including
// switching accounts or filters while an earlier request is in flight.
export function stateForRead<T>(state: ReadState<T> | undefined, key: string | undefined): ReadState<T> | undefined {
  if (key === undefined) return undefined;
  return state?.key === key ? state : {key, phase: 'loading'};
}
