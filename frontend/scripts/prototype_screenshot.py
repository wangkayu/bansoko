from playwright.sync_api import sync_playwright
import time

BASE = "http://localhost:8080"
OUT = "/tmp"


def screenshot(page, name):
    path = f"{OUT}/{name}.png"
    page.screenshot(path=path, full_page=False)
    print(f"saved {path}")


def main():
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1440, "height": 900})
        page.goto(BASE)
        page.wait_for_load_state("networkidle")

        # If we hit the login gate, log in.
        if page.locator('input[type="password"]').count() > 0:
            print("login gate detected")
            screenshot(page, "00-login")
            inputs = page.locator("input").all()
            for i, inp in enumerate(inputs):
                print(i, inp.get_attribute("type"), inp.get_attribute("placeholder"))
            if len(inputs) >= 2:
                inputs[0].fill("admin")
                inputs[1].fill("admin-password-1234")
            page.locator("button", has_text="登录").first.click()
            page.wait_for_load_state("networkidle")
            time.sleep(1)

        # Catalog stage should be visible
        page.wait_for_selector("button:text('AniBT')", timeout=15000)
        screenshot(page, "01-catalog-anibt")

        # Click Mikan tab
        page.locator("button", has_text="Mikan").first.click()
        page.wait_for_timeout(800)
        screenshot(page, "02-catalog-mikan")

        # Click Custom tab
        page.locator("button", has_text="Custom").first.click()
        page.wait_for_timeout(800)
        screenshot(page, "03-catalog-custom")

        # Back to AniBT tab
        page.locator("button", has_text="AniBT").first.click()
        page.wait_for_timeout(800)
        screenshot(page, "04-catalog-anibt-return")

        # Mobile viewport check
        page.set_viewport_size({"width": 390, "height": 844})
        page.goto(BASE)
        page.wait_for_load_state("networkidle")
        page.wait_for_selector("button:text('AniBT')", timeout=15000)
        page.locator("button", has_text="Mikan").first.click()
        page.wait_for_timeout(800)
        screenshot(page, "05-mobile-mikan")

        browser.close()


if __name__ == "__main__":
    main()
