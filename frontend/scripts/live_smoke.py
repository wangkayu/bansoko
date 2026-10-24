import os
from pathlib import Path
from playwright.sync_api import sync_playwright


BASE_URL = "http://localhost:8080"


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
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1440, "height": 900})
        console_messages: list[str] = []
        request_failures: list[str] = []
        page.on("console", lambda message: console_messages.append(f"{message.type}: {message.text}"))
        page.on("requestfailed", lambda request: request_failures.append(f"{request.method} {request.url}: {request.failure}"))
        sign_in(page)
        page.screenshot(path="frontend/playwright-report/live-home.png", full_page=True)
        print("title:", page.title())
        print("headings:", page.locator("h1,h2,h3").all_text_contents())
        print("buttons:", page.get_by_role("button").all_text_contents())
        print("alerts:", page.locator("[role=alert]").all_text_contents())
        print("console:", console_messages)
        print("request_failures:", request_failures)
        open_buttons = page.get_by_role("button", name="打开")
        if open_buttons.count():
            open_buttons.first.click()
            page.get_by_role("tab", name="元数据").click()
            page.screenshot(path="frontend/playwright-report/live-metadata-dialog.png", full_page=True)
            print("dialog tabs:", page.get_by_role("tab").all_text_contents())
            print("metadata text:", page.locator("[role=dialog]").inner_text())

        page.set_viewport_size({"width": 390, "height": 844})
        page.goto(BASE_URL + "/", wait_until="networkidle")
        page.screenshot(path="frontend/playwright-report/live-mobile.png", full_page=True)
        print("mobile horizontal overflow:", page.evaluate("document.documentElement.scrollWidth > document.documentElement.clientWidth"))
        page.goto(BASE_URL + "/activity", wait_until="networkidle")
        page.screenshot(path="frontend/playwright-report/live-activity.png", full_page=True)
        print("activity headings:", page.locator("h1,h2,h3").all_text_contents())
        print("activity alerts:", page.locator("[role=alert]").all_text_contents())
        browser.close()


if __name__ == "__main__":
    main()
