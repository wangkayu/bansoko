import type { ReactElement, ReactNode } from "react";
import { render, type RenderResult } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// Test-only helper. The app always mounts a QueryClientProvider at the root
// (src/App.tsx), so components are free to call useQuery; component tests must
// supply the same context. Retries are off so a rejected queryFn surfaces the
// error state immediately instead of after backoff.

export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  });
}

export function QueryWrapper({
  client,
  children,
}: {
  client?: QueryClient;
  children: ReactNode;
}) {
  return <QueryClientProvider client={client ?? createTestQueryClient()}>{children}</QueryClientProvider>;
}

export function renderWithQuery(ui: ReactElement, client?: QueryClient): RenderResult {
  const queryClient = client ?? createTestQueryClient();
  return render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>);
}
