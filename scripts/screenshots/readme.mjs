// The README screenshots (docs/img), 1280 px wide at 2x, phone 390x844 at 2x
const W = 1280;
export default [
  { name: 'dashboard', hash: '#/dashboard', w: W, wait: 7000, h: 900, full: true, maxH: 1330 },
  { name: 'dashboard-dark', hash: '#/dashboard', w: W, wait: 7000, dark: true, full: true, maxH: 1330 },
  { name: 'verbruik', hash: '#/verbruik', w: W, steps: "click('Jaar')", full: true, maxH: 1079 },
  { name: 'kosten', hash: '#/kosten', w: W, steps: "click('Jaar')", full: true, maxH: 1180 },
  { name: 'vergelijken', hash: '#/kosten', w: W, sel: "[byText('h2', 'Vergelijken'), qv('.compare-card')]", pad: 24 },
  { name: 'verwarming', hash: '#/verbruik', w: W, steps: "click('Verwarming'); await wait(1500); click('Verbruik'); await wait(800); click('Contractperiode')", full: true, maxH: 780 },
  { name: 'klimaat', hash: '#/klimaat', w: W, full: true, maxH: 1034 },
  { name: 'automatisering', hash: '#/automatisering', w: W, full: true, maxH: 1092 },
  { name: 'meldingen', hash: '#/meldingen', w: W, full: true, maxH: 1628 },
  { name: 'apparaten', hash: '#/apparaten', w: W, full: true, maxH: 1600 },
  { name: 'mobiel', hash: '#/dashboard', mobile: true, wait: 7000 },
  { name: 'mobiel-dark', hash: '#/dashboard', mobile: true, dark: true, wait: 7000 },
  { name: 'mobiel-kosten', hash: '#/kosten', mobile: true, steps: "pickText(1, 'Maand')" },
];
