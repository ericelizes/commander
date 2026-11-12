#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/../app"
node scripts/smoke.mjs --fake --freshness --query ws=1 --out out/activity-freshness.png --eval 'document.querySelector(".ws button").click()' --expect '(async () => {
 const rows = document.querySelectorAll(".ws").length;
 const decisions = document.querySelector(".nav-tabs").textContent;
 const checks = [];
 for (const name of ["connected-stalled", "unchanged-healthy", "failed-load", "recovery"]) {
   const c = await (await fetch("/__smoke/freshness?case=" + name)).json();
   await new Promise(r => setTimeout(r, 300));
   const warnings = document.querySelectorAll(".activity-freshness").length;
   const ok = (warnings === (c.warning ? 2 : 0)) && document.querySelectorAll(".ws").length === rows && document.querySelector(".nav-tabs").textContent === decisions;
   checks.push({name, warnings, rows, ok});
 }
 window.freshnessChecks = checks; return JSON.stringify(checks);
})()' --expect 'window.freshnessChecks.every(c => c.ok)'
