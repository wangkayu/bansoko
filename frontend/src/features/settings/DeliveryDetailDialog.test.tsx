import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../lib/i18n";
import { formatDateTime } from "@/lib/labels";
import { DeliveryDetailDialog } from "./DeliveryDetailDialog";

// The delivery detail dialog's copy contract: the trigger reuses the LogDialog record row's
//   notificationTriggerKindLabel zh mapping and the timestamp reuses the same formatDateTime
//   (zh-CN, 24-hour). It must never fall back to a raw snake_case enum, a raw ISO string, or the
// en-US toLocaleString（AM/PM）。

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

const detail = {
  delivery: { ...delivery, last_error: null },
  attempts: [
    {
      plugin_delivery_id: "delivery-1",
      attempt_number: 1,
      started_at: "2026-07-21T08:04:00Z",
      response_status: 200,
      error: null,
    },
  ],
};

let detailOverride: unknown = null;

function stubFetch() {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/plugin-deliveries/delivery-1")) {
      return new Response(JSON.stringify(detailOverride ?? detail), {
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
      <DeliveryDetailDialog deliveryID="delivery-1" onClose={() => {}} />
    </QueryClientProvider>,
  );
}

describe("DeliveryDetailDialog 文案", () => {
  beforeEach(stubFetch);
  afterEach(() => {
    vi.unstubAllGlobals();
    detailOverride = null;
  });

  it("触发器显示中文映射，不显示原始枚举", async () => {
    renderDialog();
    expect(await screen.findByText(t("settings.notifications.trigger.download_submitted"))).toBeInTheDocument();
    expect(screen.queryByText("download_submitted")).not.toBeInTheDocument();
  });

  it("尝试时间与共享 formatDateTime 输出一致，不显示原始 ISO 或 AM/PM", async () => {
    renderDialog();
    expect(await screen.findByText(formatDateTime(detail.attempts[0].started_at))).toBeInTheDocument();
    expect(screen.queryByText("2026-07-21T08:04:00Z")).not.toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/\d\s+(AM|PM)/);
  });

  it("终态失败显示原因码文案，不显示原始英文原因", async () => {
    detailOverride = {
      delivery: {
        ...delivery,
        state: "failed",
        attempt_count: 0,
        last_error_code: "notification_admission_retry_exhausted",
        last_error: "notification admission failed repeatedly and was abandoned",
      },
      attempts: [],
    };
    renderDialog();
    expect(await screen.findByText(t("reason.notification_admission_retry_exhausted"))).toBeInTheDocument();
    expect(screen.queryByText(/failed repeatedly/)).not.toBeInTheDocument();
  });
});
