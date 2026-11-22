# Energie- en watercontracten en kosten

Doel: per periode berekenen wat energie kostte, met het contract dat op dat
moment gold, en "wat als"-vergelijkingen met andere contracten maken.

## Uitgangspunten

- Basis is de kwartierhistorie (`quarter_hours`): afname en teruglevering
  (kWh) apart, gas en water per kwartier. Kosten zijn daardoor ook achteraf te
  berekenen voor alle opgeslagen historie.
- De day-ahead-markt (EPEX/SDAC) rekent sinds 1 oktober 2025 per kwartier;
  dynamische stroomprijzen sluiten dus 1-op-1 aan op de kwartierdata.
- Salderen stopt per 1 januari 2027: teruglevering wordt apart vergoed
  (en mogelijk belast met terugleverkosten). Afname en teruglevering worden
  daarom nooit tegen elkaar weggestreept vóór de berekening.

## Begrippen

**Contract** (leveranciersdeel), met geldigheid `van`–`tot` (datum):

- type `dynamisch`: marktprijs per kwartier + opslag per kWh (afname),
  vergoeding/opslag per kWh (teruglevering), gas: dagprijs + opslag per m³;
  met `electricity.hourly` rekent de leverancier per uur af: elk kwartier
  krijgt het gemiddelde van de vier kwartierprijzen van zijn klokuur
  (`HourAverage`; onbekend zolang het uur niet compleet is). Dat loopt via
  `quarter()`, dus kosten, voorschot, scenario's en de all-in prijzen van
  `/api/prices` en automatiseringen volgen het vanzelf;
- type `vast`/`variabel`: prijs per kWh (normaal/dal of enkel), per m³ gas,
  terugleververgoeding per kWh;
- vaste leveringskosten per maand (stroom en gas), eventuele
  terugleverkosten.
- `split`: stroom en gas apart (twee contracten in één, bijvoorbeeld bij twee
  leveranciers). `type` geldt dan voor stroom, `gas_type` voor gas; `advance`
  is het stroomvoorschot, `gas_advance` het gasvoorschot; elke vaste post
  heeft een `part` (`electricity`, standaard, of `gas`). Datums, vervolg
  (`continues`), afrekenjaar, btw en verwacht jaarverbruik zijn gedeeld; een
  afwijkende periode is een apart contract. Zonder `split` worden `gas_type`,
  `gas_advance` en `part` bij het opslaan gewist.

**Tijdlijn**: de reeks werkelijke contracten. Wisselen kan op elke datum;
een periode zonder contract wordt gemeld, niet stil overgeslagen.

**Overheidskosten per kalenderjaar** (handmatig in te voeren):
energiebelasting stroom en gas (per kWh / m³, staffels), vermindering
energiebelasting (per jaar, per dag toegerekend), btw-percentage.

**Regiokosten per kalenderjaar** (handmatig, afhankelijk van netbeheerder):
netbeheerkosten stroom en gas (per dag/maand, op basis van aansluiting).

**Scenario ("wat als")**: een alternatieve tijdlijn (bijv. "heel 2026 vast
contract X") die op dezelfde kwartierdata en dezelfde overheids- en
regiokosten wordt doorgerekend en naast de werkelijkheid wordt getoond.

## Prijsbronnen

- Stroom: **gebouwd** (`internal/prices`). EnergyZero (publieke API, geen
  token), dan Nord Pool (onofficiële data-portal-API) en ENTSO-E (A44, token)
  als terugval; opgeslagen in de tabel `prices` (markt `NL-DA`, EUR/MWh, per
  leverperiode, bron erbij). Een fijnere resolutie vervangt een grovere, nooit
  andersom. NL-prijzen zijn bij alle bronnen gelijk (SDAC-koppeling). Compleet
  vanaf 1 januari 2026; ontbrekende dagen worden dagelijks aangevuld.
- Historie: de gebruiker levert een SQLite-bestand met prijshistorie aan
  (vermoedelijk het `market_prices_seed.sqlite`-formaat uit Grip op Energie);
  import volgt.
- Gas (dynamisch): **gebouwd**. EnergyZero, prijs per gasdag (06:00–06:00,
  dus 23 of 25 uur bij de overgang van zomer- en wintertijd), in EUR/m³ zonder
  btw, inkoopvergoeding en energiebelasting; markt `NL-GAS` in dezelfde tabel.
  EnergyZero levert ook `all_in`-prijzen, maar die bevatten hun eigen opslag;
  kosten rekenen we zelf uit met de opslag van het eigen contract.

## Berekening (gebouwd, `internal/costs`)

Per kwartier, alle bedragen incl. btw (opslag en heffingen voert de gebruiker
incl. btw in; de marktprijs is excl. btw en krijgt het btw-percentage van het
contract erbij):

```
dynamisch: stroom = afname × (markt × (1 + btw) + opslag) + afname × energiebelasting
           terug  = teruglevering × (markt' + terugleververgoeding)
                    markt' = markt × (1 + btw) t/m 2026 (mag negatief),
                             markt zonder btw en niet onder 0 vanaf 2027;
                    het contract kan btw altijd aan of uit zetten (export_vat)
           gas    = m³ × (gasdagprijs × (1 + btw) + opslag) + m³ × energiebelasting
vast:      stroom = afname × tarief(normaal/dal) + afname × energiebelasting
                    dal = het deel dat de P1-meter in T1 telde (ook feestdagen),
                    anders volgens de klok: doordeweeks 23–7 uur en weekend
           terug  = teruglevering × terugleververgoeding
           gas    = m³ × tarief + m³ × energiebelasting
saldering (jaar < 2027), per kalenderjaar:
           gesaldeerd(kwartier) = min(cum. afname, cum. teruglevering) na − voor dit kwartier
           dynamisch: + gesaldeerd × energiebelasting
           vast:      + gesaldeerd × (tarief + energiebelasting − terugleververgoeding)
                      (tarief van het kwartier; zonder afname telt de
                      teruglevering voor normaal/dal)
vast per dag = som(maandposten) × 12 / dagen in het jaar   (vandaag naar rato)
totaal = stroom + gas + vast − terug − saldering
```

Normaal of dal volgt de meter zelf. Een P1-meter met twee tarieven telt
afname en teruglevering apart in T1 (dal) en T2 (normaal); de gateway
bewaart per kwartier welk deel in T1 viel (`import_low_kwh`,
`export_low_kwh`, uit de minuutstanden zoals het totaal, of uit de
HomeWizard-export). Zo tellen feestdagen als dal, en een kwartier waarin de
meter omschakelt naar rato. Zonder die gegevens (metingen van vóór deze
versie, sockets, kWh-meters, het kwartier waarin de gateway de T1-stand
voor het eerst zag, een prognose of het voorschot voor de toekomst) geldt
de klokregel. Het deel hoort bij het totaal waar het uit komt: een import
vult het alleen aan bij een gelijk totaal.

Ontbrekende gegevens tellen nooit als 0 maar worden per periode gemeld.
Elk verwacht kwartier is precies één van: compleet gemeten, gedeeltelijk
gemeten (waarde telt mee maar is geschat), afwijkend (een meterstand ging
terug, bijv. een reset: geen verbruik geteld) of ontbrekend (niet
meegeteld). Daarnaast: kwartieren met verbruik maar zonder prijs en dagen
zonder contract. Een periode is alleen `complete` als geen van deze
voorkomt.

Twee uitzonderingen, zodat er geen gaten gemeld worden die er niet zijn:

- **Het laatste uur.** De recorder rekent de kwartieren van het laatste uur
  elke minuut opnieuw uit (een gasstand of minuut die later binnenkomt, vult
  ze nog aan). Een kwartier telt pas als ontbrekend of geschat als het
  daarbuiten valt (`Calculator.Settle` = `Recorder.Settle()`); tot dan wordt
  het wel geprijsd, maar niet gemeld.
- **Vóór het begin van de historie.** Van een meter wordt niets verwacht
  vóór zijn eerste complete kwartier (gemeten of geïmporteerd,
  `Store.MeasuredFrom`): niet de dagen voordat hij werd toegevoegd (ook niet
  als dag zonder contract), en niet de kwartieren van de eerste dag vóór de
  start. Een import van oudere historie schuift dat begin vanzelf naar voren.
  `Gaps` geeft het begin mee als `measured_from` als het in de periode valt.

Resultaten van afgesloten dagen worden per apparaat en dag in het geheugen
bewaard, samen met de saldeerstand aan het eind van die dag. Opgeslagen
kwartieren (meting of import), prijzen en contractwijzigingen maken de cache
ongeldig vanaf de eerste geraakte dag; latere dagen in dat jaar hangen er via
de saldering van af en worden dus ook opnieuw berekend.

## Voorschot en prognose (gebouwd, `internal/costs/advance.go`)

- `advance.amounts`: maandbedragen met een datum. Elk bedrag is een
  betaling op zijn datum en daarna elke maand op dezelfde dag in zijn geheel
  (de 31e wordt in een kortere maand de laatste dag). Een bedrag met een
  latere datum in de reeks (ook in een vervolgcontract) neemt het over: vanaf
  die datum geldt het eerdere niet meer. Een betaling telt vanaf het begin
  van zijn dag als betaald, nooit naar rato; op een dag van een contract
  zonder voorschot wordt niets betaald. *Nieuwe periode* zet het bedrag van
  het vervolgcontract op de eerstvolgende betaaldag.
- Afrekenperiode: het contractjaar vanaf de `start` van het eerste contract
  in de reeks, begrensd door de `end` van het laatste contract in de reeks of
  door het volgende contract. Een reeks is een contract plus de contracten
  met `continues: true` die er direct op aansluiten (tariefwijzigingen bij
  dezelfde leverancier). Een vervolgcontract moet de dag na de einddatum van
  het vorige beginnen.
- Betaald: de betalingen met een datum in de afrekenperiode, tot en met
  vandaag; de rest van de periode is nog te betalen.
- De periode wordt per kwartier doorgerekend met `quarter()`, met de
  saldeerstand vanaf 1 januari. Per kwartier geldt de meting. Een dag met
  minstens 92% van zijn kwartieren (`day_min_pct`) telt als gemeten; de
  ontbrekende kwartieren krijgen het daggemiddelde (teruglevering naar
  daglicht, `fillDay`).
- Wat niet gemeten is, komt per veld (`import_kwh`, `export_kwh`, `gas_m3`;
  water `water_m3`) uit `Method.Sources`:
  1. `contract`: `estimate` van het contract (de laatste in de reeks met een
     waarde) × maandaandeel / dagen in de maand (profielen in
     `profiles.go`; teruglevering over de daglichturen verdeeld). Gaat
     altijd voor;
  2. `previous`: de vorige periode, de 12 maanden vóór de start van de
     afrekenperiode (los van contracten), als minstens 90%
     (`period_min_pct`) van die dagen bruikbaar is: dezelfde dag een jaar
     eerder (aangevuld met `fillDay`), of voor een dag daarvan zonder meting
     het jaarverbruik van die periode (gemeten dagen / hun profielaandeel) ×
     het profiel. Met de trend vermenigvuldigd;
  3. `none`: nooit gemeten (bijv. teruglevering zonder panelen), dus 0 en
     niet nodig;
  4. `missing`: geen prognose voor de onderdelen die dit veld nodig hebben
     (stroom: afname en teruglevering; gas: gas; zonder `split`: alles). Die
     status krijgt de gemeten kosten zonder schatting en `forecast_error`.
- Trend (`trend`, standaard aan): per veld met bron `previous` de verhouding
  van deze periode tot nu tegenover dezelfde dagen van de vorige periode
  (alleen dagen die in beide bruikbaar zijn), als er minstens 60 zulke dagen
  zijn (gas: ook 30 in oktober t/m april) en de vorige periode daarin genoeg
  verbruik had.
- Dynamische prijzen: opgeslagen prijzen waar bekend; anders het gemiddelde
  per uur van dezelfde maand vorig jaar (minimaal 20 dagen), of anders van de
  laatste 30 dagen.
- Bandbreedte: √ van de som van de kwadraten van onafhankelijke delen:
  per veld (afname: levering + energiebelasting; teruglevering: vergoeding +
  saldering; gas; water) de verwachte kosten × de verbruiksmarge van dat
  veld; het marktprijsdeel (marktprijs × btw × (afname − teruglevering), of ×
  m³ gas) van de kwartieren met een verwachte prijs × de prijsmarge (10%);
  en de geschatte dagen in het verleden (hun energieaandeel van de kosten tot
  nu) × de verbruiksmarge. Verbruiksmarge bij `previous`: de gewogen
  spreiding van de maanden van deze periode (minstens 20 vergeleken dagen)
  rond de trend, tussen 3 en 25%, bij minder dan 2 maanden 8%; bij
  `contract` 10%.
- Alle percentages staan in `internal/forecast` (`Defaults`) en zijn aan te
  passen onder Instellingen (`/api/settings/forecast`, tabel `settings`);
  `Method.Percentages` zegt per prognose wat er gebruikt is en waar het
  vandaan komt.
- Advies: (verwachte totale kosten − betaald tot nu) / resterende betalingen
  in de periode, afgerond op hele euro's; niet bij minder dan twee weken of
  zonder resterende betaling.
- Stroom en gas apart (`split`): `Calculator.Advances` geeft per onderdeel
  met voorschot een status (`part`), uit één simulatie. Het gasdeel is gas
  (levering + energiebelasting) plus de vaste posten met `part: gas` op dagen
  van een gesplitst contract (`Bucket.gasVar`/`gasFixed`); het stroomdeel is
  de rest van het totaal. De prijsmarge telt alleen voor een dynamisch
  onderdeel; ontbrekende prijzen van het andere onderdeel worden niet
  gemeld. Zonder `split` is er één status (`part` leeg) zoals voorheen.

## Scenario's (gebouwd, `internal/costs/scenario.go`)

- Een scenario is een contract zonder datums, voorschot of vervolg, opgeslagen
  in de tabel `scenarios`. Het telt nooit mee in kosten of voorschot.
- `Compare` rekent de gemeten kwartieren door met `computeWith`: dezelfde
  functie als de echte kosten, maar met een andere tijdlijn en zonder cache.
  De tijdlijn is het scenario over de perioden van de echte contracten, zodat
  beide over precies dezelfde dagen gaan.
- `rules_2027`: de regels van 2027 gelden ook voor eerdere jaren (`rulesYear`
  in `quarter()`): geen salderen, teruglevering zonder btw en niet onder 0.
- Ontbrekende metingen worden niet geschat; ze tellen voor werkelijk en
  scenario gelijk mee en worden gemeld.

## Water (gebouwd)

- Watercontracten staan in een eigen tabel (`water_contracts`) met een eigen
  tijdlijn: ze overlappen nooit met energiecontracten. Zelfde `Contract`-type
  met `type: "water"` en `water.rate_ct` / `water.tax_ct` (ct per m³ incl.
  btw), vaste kosten per maand, voorschot, `estimate.water_m3` en
  `continues` voor tariefwijzigingen.
- Een tweede `Calculator` op de watertijdlijn rekent de kwartieren van de
  watermeter door (`waterQuarter` in plaats van `quarter()`):
  `water = m³ × tarief + belaste m³ × belasting op leidingwater`, waarbij de
  belasting alleen over de eerste 300 m³ per kalenderjaar telt. Die
  jaarstand speelt de rol van de saldeerstand: elke dag hangt af van de
  dagen ervoor in zijn jaar, en de cache werkt hetzelfde.
- Een kwartier ontbreekt voor water als er geen watermeting is.
- Voorschot en prognose: dezelfde code als energie (afrekenjaar, reeks,
  vorige periode, trend, profiel), met een vrijwel vlak waterprofiel en het
  jaarverbruik in m³; geen marktprijzen, dus de marge komt alleen van het
  verbruik.
- API onder `/api/water/` (`contracts`, `costs`, `advance`); standaard de
  eerste watermeter. Geen scenario's.

## Nog te bouwen

- Staffels energiebelasting, een waarschuwing
  als een vaste terugleververgoeding onder de wettelijke 50% van het kale
  leveringstarief ligt (2027–2029).
