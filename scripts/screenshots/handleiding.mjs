// Screenshots for docs/GEBRUIKSHANDLEIDING.md (desktop 1200 px, phone 390 px, both at 2x)
const card = (t) => `has('.card', ${JSON.stringify(t)})`;
export default [
  // ---- dashboard
  { name: 'dash-live', hash: '#/dashboard', wait: 6000, sel: "[q('.hero'), q('.tiles')]" },
  { name: 'dash-kosten', hash: '#/dashboard', wait: 6000, sel: "q('.today-costs-pair')" },
  { name: 'dash-tegels', hash: '#/dashboard', wait: 7000, sel: "q('.my-tiles-section')" },
  { name: 'dash-prijzen', hash: '#/dashboard', wait: 6000, sel: "q('.price-card')" },
  { name: 'dash-apparaten', hash: '#/dashboard', wait: 6000, sel: "[...qa('.grid')].filter(g => g.querySelector('.device'))[0].querySelectorAll('.device')[0].parentElement" , maxH: 700 },
  { name: 'dash-aanpassen', hash: '#/dashboard', wait: 6000, steps: "click('Aanpassen')", sel: "[qa('.my-tiles .my-tile')[0], qa('.my-tiles .my-tile')[1]]" },
  // ---- verbruik en kosten
  { name: 'vk-keuzes', hash: '#/verbruik', sel: "q('.usage-filters')" },
  { name: 'vk-verbruik-dag', hash: '#/verbruik', sel: "[qv('.usage-panel .summary'), ...qav('.usage-panel .chart-card').slice(0, 2)]" },
  { name: 'vk-verbruik-jaar', hash: '#/verbruik', steps: "click('Jaar')", sel: "qav('.usage-panel .chart-card').filter(vis)[0]" },
  { name: 'vk-kosten', hash: '#/kosten', steps: "click('Maand')", sel: "[qv('.usage-panel .summary'), qv('.usage-panel .chart-card')]" },
  { name: 'vk-onvolledig', hash: '#/kosten', steps: "qv('.incomplete-tile').click()", sel: "qv('.gaps-card')", maxH: 900 },
  { name: 'vk-voorschot', hash: '#/kosten', sel: "qv('.advance-card')" },
  { name: 'vk-contracten', hash: '#/kosten', sel: "qv('.contract-list')" },
  { name: 'vk-contract-form', hash: '#/kosten', steps: "click('Bewerken')", sel: "q('.contract-form')", maxH: 2600 },
  { name: 'vk-vergelijken', hash: '#/kosten', sel: "[qv('.compare-card'), qv('.compare-card').parentElement.querySelector('.contract-list')]" },
  { name: 'vk-water', hash: '#/kosten/water', steps: "click('Jaar')", sel: "[qv('.usage-panel .summary'), qv('.usage-panel .chart-card')]" },
  { name: 'vk-verwarming-verbruik', hash: '#/verbruik', steps: "click('Verwarming'); await wait(1500); click('Verbruik')", sel: "qv('.usage-panel')", maxH: 1100 },
  { name: 'vk-verwarming-kosten', hash: '#/kosten/verwarming', sel: "[qv('.usage-panel .summary'), qv('.usage-panel .advance-card')]" },
  // ---- meldingen
  { name: 'meld-aanzetten', hash: '#/meldingen', sel: "qa('.card')[0]" },
  { name: 'meld-lijst', hash: '#/meldingen', sel: "[qa('.contract-list .card')[0], qa('.contract-list .card')[2]]" },
  { name: 'meld-form', hash: '#/meldingen', steps: "click('Binnen te koud')", sel: "q('.rule-form')", maxH: 1800 },
  { name: 'meld-voorbeelden', hash: '#/meldingen', sel: "[byText('h3', 'Voorbeelden'), byText('h3', 'Voorbeelden').nextElementSibling]" },
  // ---- automatisering
  { name: 'auto-kaart', hash: '#/automatisering', sel: "qa('.contract-list .card')[2]" },
  { name: 'auto-form', hash: '#/automatisering', steps: "click('Bewerken', 2)", sel: "q('.rule-form')", maxH: 3200 },
  // ---- apparaten
  { name: 'berichten', hash: '#/berichten', wait: 4000, sel: "[...qa('#view > .chart-head, #view > .card')]", maxH: 1100 },
  { name: 'berichten-icoon', hash: '#/dashboard', wait: 5000, sel: "q('.topbar')" },
  { name: 'app-lijst', hash: '#/apparaten', wait: 5000, steps: "q('.dev-row details.outages:not(.none)')?.setAttribute('open', '')", sel: "qa('.dev-row').slice(0, 3)" },
  { name: 'app-toevoegen', hash: '#/apparaten', sel: "[byText('h2', 'Toevoegen'), byText('h2', 'Toevoegen').nextElementSibling]" },
  { name: 'app-prijsbronnen', hash: '#/instellingen', wait: 3000, steps: "for (const p of qa('p')) if (p.textContent.includes('computernaam: ')) p.textContent = p.textContent.replace(/computernaam: .*$/, 'computernaam: thuis-pc.')", sel: "[byText('h2', 'Prijsbronnen'), byText('h2', 'Toegang op afstand').nextElementSibling]" },
  // ---- klimaat
  { name: 'klimaat', hash: '#/klimaat', wait: 5000, full: true, maxH: 1600 },
  { name: 'klimaat-airco', hash: '#/klimaat', wait: 5000, sel: "has('.plugin-tile', 'Airco')" },
  { name: 'klimaat-historie', hash: '#/klimaat', wait: 5000, steps: "const t = has('.plugin-tile', 'Airco'); [...t.querySelectorAll('button')].find(b => b.textContent === 'Historie').click()", sel: "has('.plugin-tile', 'Airco')", maxH: 1800 },
  { name: 'klimaat-tegel-maken', hash: '#/klimaat', wait: 5000, steps: "click('Dashboardtegel maken'); await wait(500); has('button', 'Waarde').click()", sel: "q('.my-tile-editor')" },
  // ---- thuisbatterij (the plugin "Thuisbatterij (via P1-meter)", control allowed, the example automation)
  { name: 'bat-tegel', hash: '#/klimaat', wait: 6000, sel: "has('.plugin-tile', 'Thuisbatterij')" },
  { name: 'bat-stand', hash: '#/klimaat', wait: 6000, steps: "const s = has('.plugin-tile', 'Thuisbatterij').querySelector('select'); s.value = 'discharge_only'; s.dispatchEvent(new Event('change', { bubbles: true })); await wait(8000)", sel: "has('.plugin-tile', 'Thuisbatterij')" },
  { name: 'bat-plugin', hash: '#/instellingen', wait: 6000, steps: "qa('details').forEach(d => d.open = true)", sel: "has('.plugins .plugin-card', 'Thuisbatterij')" },
  { name: 'bat-automatisering', hash: '#/automatisering', wait: 4000, steps: "click('Bewerken')", sel: "q('.rule-form')", maxH: 2400 },
  // ---- instellingen
  { name: 'inst-plugins', hash: '#/instellingen', wait: 6000, steps: "qa('details').forEach(d => d.open = true)", sel: "[q('.plugins .plugin-card')]" },
  { name: 'inst-toevoegen', hash: '#/instellingen', wait: 6000, steps: "const s = has('.card', 'Plugin toevoegen').querySelector('select'); s.value = [...s.options].find(o => o.textContent.includes('tado')).value; s.dispatchEvent(new Event('change', { bubbles: true }))", sel: "has('.card', 'Plugin toevoegen')" },
  { name: 'inst-rapporten', hash: '#/instellingen', wait: 6000, sel: "[byText('h2', 'Rapporten'), byText('h2', 'Rapporten').nextElementSibling]" },
  { name: 'inst-tabvolgorde', hash: '#/instellingen', wait: 6000, steps: "q('.tab-order-box').open = true", sel: "[byText('h2', 'Tabbladen'), byText('h2', 'Tabbladen').nextElementSibling]" },
  { name: 'inst-tabbladen', hash: '#/instellingen', wait: 6000, sel: "has('.card', 'Eigen tabbladen')" },
  // ---- phone (also the README phone shots)
  { name: 'tel-dashboard', hash: '#/dashboard', mobile: true, wait: 6000 },
  { name: 'tel-tegels', hash: '#/dashboard', mobile: true, wait: 7000, steps: "scrollTo(0, q('.my-tiles-section').getBoundingClientRect().top + scrollY - 8)", sel: "q('.my-tiles-section')", maxH: 1500 },
  { name: 'tel-kosten', hash: '#/kosten', mobile: true },
  { name: 'tel-klimaat', hash: '#/klimaat', mobile: true, wait: 5000, sel: "has('.plugin-tile', 'Airco')", maxH: 1500 },
  { name: 'tel-instellingen', hash: '#/instellingen', mobile: true, wait: 6000, sel: "[q('.plugins .plugin-card'), qa('.plugins .plugin-card')[1]]" },
];
