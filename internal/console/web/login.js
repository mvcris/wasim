"use strict";

const form = document.getElementById("login-form");
const error = document.getElementById("login-error");

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  error.textContent = "";
  const button = form.querySelector("button");
  button.disabled = true;
  try {
    const response = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: form.email.value, password: form.password.value }),
    });
    if (response.ok) {
      window.location.assign("/");
      return;
    }
    const data = await response.json().catch(() => null);
    error.textContent = data?.error ?? `Login failed with HTTP ${response.status}`;
    form.password.select();
  } catch {
    error.textContent = "Could not reach wasim. Is it still running?";
  } finally {
    button.disabled = false;
  }
});
