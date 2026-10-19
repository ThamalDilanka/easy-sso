// Optional browser regression test; the demo itself has no dependencies.
"use strict";
const assert = require("node:assert/strict");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const { chromium } = require(
  process.env.EASY_SSO_PLAYWRIGHT_MODULE || "playwright",
);

(async () => {
  const browser = await chromium.launch({
    headless: true,
    ...(process.env.EASY_SSO_CHROMIUM
      ? { executablePath: process.env.EASY_SSO_CHROMIUM }
      : {}),
    args: ["--no-sandbox"],
  });
  try {
    const context = await browser.newContext({
      offline: true,
      viewport: { width: 1440, height: 1000 },
    });
    const requests = [],
      errors = [];
    context.on("request", (request) => {
      if (/^https?:/.test(request.url())) requests.push(request.url());
    });
    const page = await context.newPage();
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(
      pathToFileURL(path.resolve(__dirname, "../docs/demo/easy-sso-demo.html"))
        .href,
    );
    const app = page.locator("#app");
    const pane = page.locator("#pane");
    const status = page.locator("[role=status]");
    const action = (key) => pane.locator(`[data-key="${key}"]`).first().click();
    const nav = (view) => page.locator(`[data-view="${view}"]`).click();
    const workspace = () =>
      page.locator('.shortcut-buttons [data-key="ctrl+]"]').click();
    const idle = () =>
      page.waitForFunction(
        () =>
          !document
            .querySelector("[role=status]")
            ?.textContent.includes("in progress"),
      );
    const has = async (locator, text) =>
      assert.ok(
        (await locator.innerText()).includes(text),
        `Expected ${JSON.stringify(text)} in ${await locator.innerText()}`,
      );
    const reset = async (scenario = "logged-out") => {
      await page.locator("#scenario").selectOption(scenario);
      await page.locator("#reset").click();
    };
    const key = async (value) => {
      await app.focus();
      await page.keyboard.press(value);
    };
    const field = (index) => page.locator(`#field-${index}`);

    // Login gating, canned shell, duplicate focus, frozen identity, stop review.
    await has(app, "Logged out (no active keys)");
    await action("enter");
    await has(status, "Log in with Ctrl+A");
    await page.locator('.identity [data-key="ctrl+a"]').click();
    await idle();
    await has(app, "Role credentials: active");
    await action("enter");
    await page.locator("#command").waitFor();
    await page.locator("#command").fill("aws sts get-caller-identity");
    await page.locator("#command").press("Enter");
    await has(page.locator("#terminal-output"), "ReadOnlyAccess");
    await workspace();
    await action("enter");
    assert.equal(await page.locator("[data-close]").count(), 1);
    await workspace();
    await page.locator("[data-profile=admin]").click();
    await nav(2);
    await has(pane, "Run profile: default");
    await action("d");
    await has(pane, "stop only this run");
    await action("enter");
    await has(pane, "Stopped");
    assert.equal(await page.locator("[data-close]").count(), 0);

    // Editing never reconnects; text is escaped; drafts survive cancel.
    await reset("active");
    await action("e");
    await field(0).fill(
      '<img src="https://example.invalid/probe" onerror="alert(1)">',
    );
    await field(0).press("Control+s");
    await has(pane, "<img src=");
    assert.equal(await app.locator("img").count(), 0);
    assert.equal(await page.locator("[data-close]").count(), 0);
    await action("n");
    await field(0).fill("Example draft");
    await field(0).press("Escape");
    await action("n");
    assert.equal(await field(0).inputValue(), "Example draft");
    await field(1).fill("i-00000000000000005");
    await field(3).press("Enter");
    await has(pane, "Review connection");
    await action("enter");
    await page.locator("#command").waitFor();
    await workspace();
    await has(pane, "Example draft");

    // EC2 session documents are optional, checked, and always visible.
    await reset("active");
    await nav(0);
    await has(pane, "Default (Session Manager preferences)");
    await action("n");
    assert.equal(
      await page.locator('label[for="field-3"] span').innerText(),
      "Session document (optional)",
    );
    await field(0).fill("Example shell");
    await field(1).fill("i-00000000000000006");
    await field(3).fill("AWS-StartSSHSession");
    await field(3).press("Enter");
    await has(status, "not a shell session document");
    await field(3).fill("  Example-ShellDocument  ");
    await field(3).press("Enter");
    await has(pane, "Review connection");
    await has(pane, "Example-ShellDocument");
    await reset("active");
    await nav(5);
    await action("i");
    await field(0).press("Enter");
    await has(pane, "| document Example-ShellDocument");

    // Database tunnel/IAM setup and occupied local port.
    await reset("active");
    await nav(1);
    await action("enter");
    await page.locator("#command").waitFor();
    await page.locator("#command").fill("select current_user;");
    await page.locator("#command").press("Enter");
    await has(page.locator("#terminal-output"), "example_reader");
    await workspace();
    await pane.locator('[data-row="1"]').click();
    await action("e");
    await field(6).fill("15432");
    await field(6).press("Control+s");
    await action("enter");
    await idle();
    await has(status, "local port is already used");
    await action("e");
    await field(6).fill("15433");
    await field(6).press("Control+s");
    await action("enter");
    await idle();
    await has(status, "database login not attempted");
    await action("l");
    await field(8).fill("example");
    await field(9).fill("example_reader");
    await field(9).press("Control+s");
    await action("enter");
    await page.locator("#command").waitFor();
    assert.equal(await page.locator("[data-close]").count(), 2);
    await workspace();
    await nav(3);
    assert.equal(await pane.locator("[data-row]").count(), 2);

    // Multi-role activation requires acknowledgement; renew retains alias.
    await reset();
    await page.locator(".shortcut-buttons [data-key=A]").click();
    await page.locator("[data-pick=admin]").check();
    await action("enter");
    await field(2).focus();
    await field(2).press("Enter");
    await has(status, "Acknowledge");
    await page.locator("[data-ack]").check();
    await page.locator("[data-ack]").press("Enter");
    await idle();
    await nav(4);
    assert.equal(await pane.locator("[data-row]").count(), 2);
    await action("r");
    await idle();
    await has(status, "Selected role credentials published");
    assert.equal(await pane.locator("[data-row]").count(), 2);

    // Logout does not terminate established shells.
    await nav(0);
    await action("enter");
    await page.locator("#command").waitFor();
    await workspace();
    await page.locator('.identity [data-key="ctrl+a"]').click();
    await action("enter");
    await has(app, "Logged out (no active keys)");
    assert.equal(await page.locator("[data-close]").count(), 1);

    // Virtual backup merge defaults to keep; replacement requires opt-in.
    await reset("active");
    await nav(5);
    await action("i");
    await field(0).press("Enter");
    await has(pane, "Additions: 1 | Conflicts: 1");
    await action("enter");
    await nav(0);
    assert.equal(await pane.locator("[data-row]").count(), 3);
    assert.ok(!(await pane.innerText()).includes("(imported label)"));
    await nav(5);
    await action("i");
    await field(0).press("Enter");
    await page.locator("[data-replace]").check();
    await action("enter");
    await nav(0);
    await has(pane, "Example web server (imported label)");
    await nav(5);
    await action("e");
    await field(0).press("Enter");
    await has(status, "Saved in memory as demo/exported-bookmarks.json");
    await action("e");
    await field(0).press("Enter");
    await has(status, "already exists");
    await field(0).press("Escape");
    await action("i");
    await field(0).fill("demo/exported-bookmarks.json");
    await field(0).press("Enter");
    await has(pane, "Additions: 0 | Conflicts: 7");

    // Selecting a scenario only applies on reset; failures and retry are usable.
    await page.locator("#scenario").selectOption("expired");
    await has(page.locator("#scenario-hint"), "Click Reset demo to apply");
    await has(app, "Role credentials: active");
    await page.locator("#reset").click();
    await has(app, "keys expired");
    await page.locator('.identity [data-key="ctrl+a"]').click();
    await idle();
    await has(app, "Role credentials: active");
    await reset("cleanup-failure");
    await page.locator('.identity [data-key="ctrl+a"]').click();
    await idle();
    await has(status, "simulated SSO cleanup failure");
    await nav(4);
    await action("c");
    await idle();
    await page.locator('.identity [data-key="ctrl+a"]').click();
    await idle();
    await has(app, "Role credentials: active");
    await reset("connection-failure");
    await action("enter");
    await idle();
    await has(status, "simulated connection failure");
    assert.equal(await page.locator("[data-close]").count(), 0);

    // Escape cancels in-flight work and reset cannot receive stale callbacks.
    await reset();
    await page.locator('.identity [data-key="ctrl+a"]').click();
    await key("Escape");
    await page.waitForTimeout(350);
    await has(app, "Logged out (no active keys)");
    await page.locator('.identity [data-key="ctrl+a"]').click();
    await reset();
    await page.waitForTimeout(350);
    await has(app, "Logged out (no active keys)");

    await key("?");
    await has(pane, "Keyboard help");
    await key("Escape");
    await page.locator("#theme").click();
    assert.equal(
      await page.locator("html").getAttribute("data-theme"),
      "light",
    );
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await app.evaluate((el) => el.clientWidth >= 760));
    await key("q");
    await app.locator("[data-key=enter]").click();
    await has(app, "Workspace exited");
    await reset();
    await page.reload();
    await has(app, "Logged out (no active keys)");
    assert.deepEqual(errors, []);
    assert.deepEqual(requests, []);
    console.log(
      "Offline demo: all interaction checks passed; zero HTTP(S) requests and zero page errors.",
    );
  } finally {
    await browser.close();
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
