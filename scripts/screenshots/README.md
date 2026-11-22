# Screenshots

The images in `docs/img` (README) and `docs/img/handleiding` (the user guide,
`docs/GEBRUIKSHANDLEIDING.md`) are made from the simulator with demo data, so
they can be made again after a UI change.

1. Start the simulator with a data dir of its own and a price backfill from the
   start of the year:

   ```bash
   HWLOCAL_PRICE_BACKFILL_FROM=2026-01-01 scripts/dev-sim.sh data/shots
   ```

2. Fill it once with demo history, device names, contracts and a scenario:

   ```bash
   node scripts/screenshots/demo-data.mjs
   ```

   The rest of the demo was set up by hand in the app and is kept in
   `data/shots` (not in git): the MHI, Tuya and ista plugins (ista:
   `sim@example.org` / `sim`, all radiators added, contracts for 2025/26 with
   a settlement and 2026/27 with an advance), control allowed for the living
   room air conditioner, the alerts and automations, and a few own dashboard
   tiles. Pair the battery after every restart of the simulator (Apparaten →
   Koppelen).

3. Shoot:

   ```bash
   node scripts/screenshots/shoot.mjs docs/img scripts/screenshots/readme.mjs
   node scripts/screenshots/shoot.mjs docs/img/handleiding scripts/screenshots/handleiding.mjs
   ```

   A third argument shoots only the shots whose name contains it. Each shot
   lists its page, the clicks to make (`steps`) and what to cut out (`sel`,
   with a little margin); without `sel` it is the top of the page.

`shoot.mjs` drives headless Edge over the DevTools protocol (Windows path in
the script; Node 22 or later). It clears the app's local storage before every
shot, so remembered choices do not carry over.
