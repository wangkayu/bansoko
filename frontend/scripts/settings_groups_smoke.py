import os
from pathlib import Path

from playwright.sync_api import sync_playwright


BASE_URL = "http://localhost:8080"
REPORT_DIR = Path("frontend/playwright-report")


def sign_in(page) -> None:
    """The browser session is an HttpOnly cookie, so a scratch script cannot
    seed it from localStorage; sign in the way a person would instead."""
    page.goto(BASE_URL + "/", wait_until="networkidle")
    if page.get_by_label("用户名").count() == 0:
        return  # skip-auth development instance: no gate
    page.get_by_label("用户名").fill(os.environ["BANSOKO_E2E_USERNAME"])
    page.locator('input[type="password"]').first.fill(os.environ["BANSOKO_E2E_PASSWORD"])
    page.get_by_role("button", name="登录").click()
    page.wait_for_load_state("networkidle")


def main() -> None:
    REPORT_DIR.mkdir(parents=True, exist_ok=True)
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1440, "height": 900})
        failures: list[str] = []
        responses: list[str] = []
        page.on("requestfailed", lambda request: failures.append(f"{request.method} {request.url}: {request.failure}"))
        page.on("response", lambda response: responses.append(f"{response.status} {response.request.method} {response.url}") if "/api/v1/" in response.url else None)

        sign_in(page)
        page.screenshot(path=str(REPORT_DIR / "live-settings-groups.png"), full_page=True)
        print("settings nav:", page.locator('a[title="设置"]').count())
        page.locator('a[title="设置"]').click()
        page.get_by_role("button", name="订阅与自动化").click()
        page.wait_for_timeout(250)
        print("group policy text:", page.locator("[role=dialog]").inner_text())
        print("policy save buttons:", page.get_by_role("button", name="保存策略").count())

        page.goto(BASE_URL + "/", wait_until="networkidle")
        page.get_by_role("button", name="Mikan").click()
        page.wait_for_load_state("networkidle")
        print("mikan url:", page.url)
        print("api responses:", responses[-20:])
        print("mikan buttons:", page.get_by_role("button").all_text_contents())
        print("mikan cards:", page.locator("article").all_text_contents())
        open_buttons = page.get_by_role("button", name="打开")
        subscribe_buttons = page.get_by_role("button", name="订阅")
        if open_buttons.count() or subscribe_buttons.count():
            (open_buttons.first if open_buttons.count() else subscribe_buttons.first).click()
            page.get_by_role("tab", name="字幕组").click()
            page.screenshot(path=str(REPORT_DIR / "live-mikan-groups.png"), full_page=True)
            print("groups tab text:", page.locator("[role=dialog]").inner_text())
            print("refresh group buttons:", page.get_by_role("button", name="刷新列表").count())

        print("request failures:", failures)
        browser.close()


if __name__ == "__main__":
    main()
