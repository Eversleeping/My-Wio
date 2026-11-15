export type SessionGroup<T> = {
  id: string; name: string; path: string; serverName: string;
  pinnedAt?: string | null; count: number; context?: T; threads: T[];
};
