#!/bin/bash
# Layout check for the jump arrow: scrolls the workflow so the current step is off screen and asserts (elementFromPoint) the arrow is the topmost element.
# usage: scripts/jump-check.sh <query: ws=1&flow=1 | ask=1> <up|down> <dark|light> <tag>   (H=<window height>; down needs a short window, e.g. H=250)
cd "$(dirname "$0")/.."
export CHROME_HEADLESS_SHELL=${CHROME_HEADLESS_SHELL:-/usr/bin/google-chrome}
q=$1; dir=$2; theme=$3; tag=$4
if [ $dir = up ]; then at=above; where='p.scrollHeight'; else at=below; where='0'; fi
# up arrow = Now step above viewport -> scroll to bottom; down arrow -> scroll to top
EV="(async()=>{const sleep=ms=>new Promise(r=>setTimeout(r,ms));let p=document.querySelector('.dg').parentElement;while(p&&!/auto|scroll/.test(getComputedStyle(p).overflowY))p=p.parentElement;p.scrollTop=$where;p.dispatchEvent(new Event('scroll'));await sleep(700)})()"
EX="(()=>{const b=document.querySelector('.dg-jump-rail.$at .dg-jump');if(!b)return 'no button';const r=b.getBoundingClientRect();const e=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return (e===b||b.contains(e))?'TOPMOST':'COVERED by '+(e&&e.className)})() === 'TOPMOST'"
EX2="(()=>{const b=document.querySelector('.dg-jump-rail.$at .dg-jump');if(!b)return 'no button';const r=b.getBoundingClientRect();const e=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return (e===b||b.contains(e))?'TOPMOST':'COVERED by '+(e&&e.className)})()"
CHROME_HEADLESS_SHELL=$CHROME_HEADLESS_SHELL node scripts/smoke.mjs --fake --fixture ${FIXTURE:-test/fixtures/snapshot-workflow.json} --query "$q" --theme $theme --height ${H:-360} --out /tmp/jump-$tag.png --eval "$EV" --expect "$EX2" --expect "$EX" 2>&1 | grep -v ENOTEMPTY
