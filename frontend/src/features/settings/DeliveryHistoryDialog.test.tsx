import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../lib/i18n";
import { formatDateTime } from "@/lib/labels";
import { DeliveryHistoryDialog } from "./DeliveryHistoryDialog";

// The delivery history list's copy contract: the in-row trigger reuses the LogDialog record row's
//   notificationTriggerKindLabel zh mapping and the timestamp reuses the same formatDateTime
//   (zh-CN, 24-hour). It must never fall back to a raw snake_case enum or the browser's en-US
//   toLocaleString (e.g. 7/21/2026, 4:00:00 PM).

const delivery = {
  id: "delivery-1",
  plugin_target_config_id: "target-1",
  plugin_target_config_version: 1,
  adapter_kind: "telegram",
  adapter_config_snapshot: { adapter_kind: "telegram", chat_id: "42" },
  delivery_kind: "business",
  trigger_kind: "download_submitted",
  source_owner_type: "download_task",
  source_owner_id: "task-1",
  source_owner_version: 1,
  source_result_key: "result-1",
  managed_anime_id: "anime-1",
  state: "delivered",
  version: 1,
  idempotency_key: "idem-1",
  payload_schema_version: 1,
  payload: { schema_version: 1, trigger_kind: "download_submitted", facts: {} },
  payload_status: "available",
  payload_digest: "digest-1",
  template_language_version: "bansoko-template-1",
  template_catalog_version: "notification-catalog-v1",
  title_template_snapshot: "{title}",
  body_template_snapshot: "{body}",
  target_display: "测试频道",
  attempt_count: 1,
  created_at: "2026-07-21T08:00:00Z",
  updated_at: "2026-07-21T08:05:00Z",
  retention_until: "2026-07-28T08:00:00Z",
};

function stubFetch() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/plugin-deliveries")) {
      return new Response(JSON.stringify({ items: [delivery], total: 1 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(JSON.stringify({}), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
}

function renderDialog() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <DeliveryHistoryDialog onClose={() => {}} />
    </QueryClientProvider>,
  );
}

describe("DeliveryHistoryDialog 文案", () => {
  beforeEach(stubFetch);
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("行内触发器显示中文映射，不显示原始枚举", async () => {
    renderDialog();
    expect(await screen.findByText(t("settings.notifications.trigger.download_submitted"))).toBeInTheDocument();
    expect(screen.queryByText("download_submitted")).not.toBeInTheDocument();
  });

  it("时间戳与共享 formatDateTime 输出一致，不显示 en-US AM/PM 格式", async () => {
    renderDialog();
    expect(await screen.findByText(formatDateTime(delivery.created_at))).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/\d\s+(AM|PM)/);
    expect(screen.getByText(t("settings.delivery.state.delivered"))).toBeInTheDocument();
  });
});
