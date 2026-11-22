# Verwarming via mijn.ista.nl

Doel: het verwarmingsverbruik uit de warmtekostenverdelers van ista (één per
radiator) in de app zien, per kamer en per dag, week, maand en afrekenperiode,
alles lokaal opgeslagen, en per afrekenperiode de kosten volgen: een schatting
met voorschot tijdens de periode, definitief na de afrekening.

Status: **gebouwd** (branch `feat/ista`). Onderzocht op 2026-11-11 met een
losse probe tegen een echt account (6 radiatoren, dienst Verwarming, doprimo 3
radio); gebouwd en getest tegen een nagebootste ista (`ista.Sim`, ook in
`hwsim`). Gebruikersuitleg staat in de [handleiding](HANDLEIDING.md).

## Wat ista levert (onderzocht)

Geen officiële API. mijn.ista.nl is een webapp met een JSON-API erachter; de
community-bibliotheek `aalaei/mijn-ista-api` gebruikt dezelfde calls. De site
zelf gebruikt niets anders (alle scripts doorzocht).

**Inloggen.** OpenID Connect via Keycloak (`login.ista.com`, realm
`portal_nl`): `GET /` → `GET /home/login` (met navigatie-headers, anders 401)
→ Keycloak-formulier `kc-form-login` posten met gebruikersnaam en wachtwoord →
het teruggegeven formulier (`code`, `state`) terugposten → `/Home`. Daarop
staat `<meta name="csrf-token">`. Er is geen refresh-token of API-sleutel:
**de gateway moet gebruikersnaam en wachtwoord bewaren** en opnieuw inloggen
als de sessie verloopt.

**Data-calls**, allemaal `POST` met JSON, de sessiecookies, header
`x-csrf-token-ista-nl_tp` en een vaste `Authorization: Bearer`-tekst die de
site zelf ook stuurt:

| Call | Body | Geeft |
|---|---|---|
| `/api/Values/UserValues` | `LANG` | adressen (`Cuid`, intrekdatum), afrekenperiodes (`y`, `s`, `e`, gem. buitentemp `ta`), diensten (eenheid), lopend seizoen tegen vorig jaar, per radiator |
| `/api/Values/ConsumptionValues` | `Cuid`, `Billingperiod` | per radiator: kamer, meternummer, beginstand, eindstand, tikken (`CValue`), factor (`CalcFactor`), korting (`Reduction`), eenheden (`CCValue` = tikken × factor, `CCDValue` = na korting) |
| `/api/Consumption/MonthValues` | `Cuid` | per maand: eenheden totaal (geen kamers), gebouwgemiddelde, buitentemperatuur, geschat ja/nee; laadt in stukken (`hs`/`sh`, soms HTTP 425) |
| `/api/Values/ConsumptionAverages` | `Cuid`, `PAR.start`, `PAR.end` | gebouwgemiddelde (`NormalizedValue`) over dat venster |

**De sleutel:** `ConsumptionValues` accepteert in `Billingperiod.e` élke
datum binnen de periode en geeft dan de standen van die dag (getest per dag
over januari 2026 en per week over alle periodes; komt exact overeen met een
zelf bijgehouden weektabel). `ConsumptionAverages` met `start` = begin van
de periode en `end` = die dag geeft het cumulatieve gebouwgemiddelde. Zo is
de **volledige dag-historie per kamer** vanaf de intrek op te halen.

Bijzonderheden:

- Standen zijn hele getallen; de stand van **vandaag** heeft decimalen (een
  tussenstand) en een veld `EvalR` (betekenis onbekend).
- Het **gebouwgemiddelde wordt achteraf bijgesteld** (februari 2026 was toen
  1846, nu 1965). Eigen standen veranderen achteraf niet.
- Een afrekenperiode loopt hier van 1 juli t/m 30 juni; de eerste begint bij
  de intrekdatum. Factor en korting horen bij de meter en kunnen per periode
  verschillen.
- Eenheden zijn een verdeelsleutel, geen energie: er is geen omrekening naar
  kWh of GJ.

## Beslissingen

### I1 — Plugin-type voor de koppeling, eigen pakket voor data en kosten

ista is geen live-apparaat: één keer per dag nieuwe gegevens, historie die
nooit weg mag, en eigen kosten. Het plugin-framework past voor de koppeling
(account invullen onder Plugins, apparaten, tegels, regels), maar niet voor de
opslag: `plugin_samples` wordt opgeschoond na `HWLOCAL_RETENTION_DAYS` en een
poll is hooguit eens per uur.

Daarom:

- **`internal/ista`**: client (inloggen + de vier calls), synchronisatie,
  opslag-queries, berekeningen (verbruik per periode, kosten, prognose).
- **Provider `ista`** in het plugin-framework: instance-formulier met
  gebruikersnaam en wachtwoord (`Secret`), scan = "Radiatoren ophalen" (één
  apparaat per radiator, plus één "Totaal" per adres). `Poll` leest **alleen
  de lokale database** (geen netwerk) en geeft metrics als "eenheden vandaag /
  deze week / dit seizoen", "zelfde moment vorig jaar", "gebouw", "laatste
  uitlezing". Zo werken tegels en regels zonder extra code.
- De synchronisatie is een eigen dienst (`ista.Service.Run`, gestart in
  `cmd/hwlocal/main.go`), niet een taak van de instance: de plugin-manager
  bouwt instances bij elke wijziging opnieuw op. De dienst leest de
  ingeschakelde ista-instances (gebruikersnaam, wachtwoord) zelf uit de store.
  De provider wordt in `main.go` geregistreerd met die dienst
  (`plugin.Register(ista.Provider{Svc})`) in plaats van in een `init`; zo is
  `plugin.Env` niet uitgebreid. Na nieuwe gegevens publiceert de dienst een
  `ista`-event op de stream en pollt de manager de ista-apparaten meteen
  (`Manager.PollType`).

### I2 — Alles opslaan, ook wat maar één keer op te halen is

Eén nieuwe migratie (18, achteraan de slice):

- `ista_meters` — per radiator: `meter_id` (ista `MeterId`, sleutel), `cuid`,
  `service_id`, `rad_nr`, `position` (kamer), `meter_nr`, `art_nr`, eenheid.
- `ista_meter_periods` — per meter per afrekenperiode: `calc_factor`,
  `reduction`, `multiply`, `transfer_loss`, `begin_value`.
- `ista_readings` — per meter per dag (`meter_id`, `day`): `c_value` (tikken
  sinds begin periode, REAL), `cc_value`, `ccd_value`, `end_value`,
  `estimated` (tussenstand van vandaag), `fetched_at`. Cumulatief binnen de
  periode; verbruik per dag = verschil met de dag ervoor.
- `ista_days` — per adres per dag: `total_now`, `building_avg` (cumulatief
  sinds begin periode), `fetched_at`.
- `ista_months` — per adres, dienst en maand: totaal, gebouwgemiddelde,
  buitentemperatuur, geschat.
- `ista_addresses` — per adres (`cuid`): het account (plugin-instance),
  adres en intrekdatum.
- `ista_periods` — per adres per afrekenperiode: `y`, `start_day`, `end_day`,
  `ta`.
- `ista_services` — de diensten met hun eenheid.
- `ista_sync` — per account de stand van synchroniseren (laatste keer, laatste
  keer gebouwgemiddelden, of de historie compleet is, en een vingerafdruk van
  geweigerde inloggegevens), zodat een herstart niets herhaalt of overslaat.
- `ista_raw` — het laatste ruwe antwoord per call en sleutel (`endpoint`,
  `key`, `fetched_at`, `body`), zodat velden die we nu niet gebruiken
  (zoals `EvalR`) later niet opnieuw opgehaald hoeven te worden.

### I3 — Synchroniseren

- **Eenmalig (backfill)**: elke dag van de intrekdatum tot vandaag die nog
  niet in `ista_readings` staat: `ConsumptionValues` + `ConsumptionAverages`.
  Bij twee jaar is dat ±1500 calls; met 700 ms pauze tussen calls (`Pace`,
  `HWLOCAL_ISTA_PACE`) ruim een half uur, op de achtergrond en hervatbaar
  (alleen ontbrekende dagen, een dag zonder gebouwgemiddelde of een
  tussenstand). Het gebouwgemiddelde loopt vanaf de intrek als die in de
  periode valt. Eerst `MonthValues` (één call: meteen een maandoverzicht),
  dan de dagen **nieuwste eerst**, de lopende periode voor de oudere, zodat
  de app snel iets bruikbaars toont. De voortgang (`done`/`todo` en een
  geschatte resttijd `remaining_s`) staat in `GET /api/ista` en wordt
  getoond op de Verwarming-pagina en bij de plugin onder Instellingen.
- **Dagelijks** (vanaf 06:00 lokale tijd, bij een fout na 30 minuten
  opnieuw): `UserValues`, de **laatste 14 dagen** opnieuw (ista werkt recente
  standen bij; een tussenstand wordt vervangen) en `MonthValues`.
- **Wekelijks**: het gebouwgemiddelde van alle dagen van de lopende periode
  opnieuw (ista stelt het bij). Afgesloten periodes nog drie maanden na hun
  einde wekelijks, daarna niet meer.
- Eén login per synchronisatie; bij 401 opnieuw inloggen; bij een
  geweigerd wachtwoord stoppen en de instance op "inloggen mislukt" zetten
  (niet blijven proberen, Keycloak kan het account blokkeren).

### I4 — Weergave

Berekend uit de opgeslagen dagstanden, niet apart opgeslagen:

- **Gemeten of geschat**: een dagstand met decimalen is een schatting van
  ista (een tussenstand van vandaag, of dagen waarop ista de radiatoren niet
  uitlas; dan lopen de standen daarna niet meer op en is het
  gebouwgemiddelde 0). `GET /api/ista` geeft per adres `last_day` (laatste
  opgeslagen dag) en `last_measured` (laatste dag zonder schatting).
  Verbruik, weektabel, kosten (`/api/ista/costs`, ook de kosten tot nu toe
  en het startpunt van de prognose), de plugin en het rapport stoppen bij
  `last_measured`: schattingen (en vandaag) tellen nergens mee. De dagelijkse
  sync van de laatste 14 dagen vult ze aan zodra ista ze gemeten heeft;
  `hwsim -ista-stale N` simuleert dit.

- Per kamer en totaal, in **eenheden** en in **tikken**, per dag, week,
  maand en afrekenperiode.
- Vergelijking met dezelfde dagen vorig jaar en met het gebouwgemiddelde.
- Een **weektabel** zoals de eigen spreadsheet: per week de stand per kamer,
  totaal, verbruik in die week, gebouw en verschil.
- Plek in de PWA: een soort "Verwarming" in **Verbruik en Kosten**
  (`usage.js`), naast energie en water en met dezelfde keuzes bovenaan: de
  kamer op de plek van het apparaat, de periode (7 dagen, Maand, Jaar,
  Contractperiode = een afrekenperiode van ista, Alles, en Gisteren: één dag
  met een kolom per kamer, of een melding met de laatst beschikbare dag als
  ista die dag nog niet heeft; geen Vandaag) en de weergave (Verbruik,
  Kosten; Weken is vervallen, `GET /api/ista/weeks` bestaat nog). Het paneel zelf staat in
  `heating.js`. Kosten ziet eruit als bij water: totaal, verbruik en vaste
  kosten van de gekozen dagen (`GET /api/ista/costs?from&to&bucket`), de
  voorschotkaart van de afrekenperiode en de lijst *Verwarmingscontracten*
  (één per afrekenperiode) met hetzelfde soort formulier. Zichtbaar zodra er
  ista-apparaten of ista-gegevens zijn. Plus een kaart *Verwarming dit
  seizoen* op het dashboard.
- Een week of maand wordt afgekapt op de randen van het gevraagde bereik
  (de eerste week begint op de intrekdag).

API: `GET /api/ista` (accounts met status en voortgang, adressen met kamers
en periodes), `POST /api/ista/sync`, `GET /api/ista/usage?cuid&from&to&bucket`,
`GET /api/ista/weeks?cuid&y`, `GET /api/ista/costs?cuid&y` (of met
`from&to&bucket` de kosten per dag/week/maand/periode),
`GET/PUT/DELETE /api/ista/contracts[/{y}]`; zie de [handleiding](HANDLEIDING.md).

### I5 — Kosten per afrekenperiode

**Hoe ista afrekent** (jaarafrekening 1-7-2025 t/m 30-6-2026 als
voorbeeld):

```
complex:  variabele energiekosten (65%)           15.748,88 / 75.970 eenheden   = 0,207304 per eenheid
          vaste energiekosten (35%)   8.480,16
          overige kosten              5.006,84
          diensten ista Nederland     2.686,25
                                     ---------
                                     16.173,25 / 170.400 dm² vloeroppervlak     = 0,094913 per dm²
woning:   1.785 eenheden × 0,207304 = 370,04
          7.100 dm²      × 0,094913 = 673,88
          totaal                     1.043,92
```

De afrekening toont per radiator dezelfde getallen als de API (stand,
factor, "verbruik" = `CCValue`, reductie, afrekenverbruik = `CCDValue`).
Het objectnummer van de afrekening is `Item1` in de (base64-JSON) `CuToken`
van `MonthValues`.

Een **ista-contract per afrekenperiode** (eigen tabel `ista_contracts`,
sleutel adres + `y`), los van de energie- en watercontracten, met precies
deze posten:

- Complex: variabele energiekosten, vaste energiekosten, overige kosten,
  diensten ista (bedragen), totaal eenheden complex, totaal vloeroppervlak
  complex (dm²). Daaruit volgen de prijs per eenheid en per dm², zoals op de
  afrekening.
- Woning: eigen vloeroppervlak (dm²); de eenheden komen uit de metingen.
- Voorschot: maandbedragen met datum, dezelfde betekenis als
  `advance.amounts` bij energie (die logica wordt gedeeld).
- Status **schatting** of **definitief**. Definitief maken = de getallen van
  de jaarafrekening invoeren; het eindbedrag moet dan op de cent kloppen met
  de afrekening (controle in het formulier). Dan telt de afrekening, niet de
  berekening.

Tijdens de periode (schatting):

- Prijzen: die van de laatste definitieve afrekening, met een optionele
  correctie in procenten (bijv. hogere gasprijs voor het complex). Het
  vaste deel (vloeroppervlak × prijs per dm²) is daarmee vrijwel bekend en
  wordt naar rato van de dagen toegerekend.
- Kosten tot nu = eenheden tot nu × prijs per eenheid + vaste deel naar
  rato.
- Prognose voor de rest van de periode, in deze volgorde (de regels van
  `internal/forecast`, gelijk aan stroom, gas en water):
  1. het **verwachte jaarverbruik** van het contract (`estimate_units`,
     eenheden per periode) × het aandeel van die dagen in een stookprofiel.
     Het profiel komt uit de maandcijfers van het gebouw
     (`ista_months.building_avg`, de laatste twaalf complete maanden), anders
     een vast graaddagenprofiel (`standardProfile`: mei t/m september vrijwel
     0, het meeste in december en januari);
  2. anders de vorige ista-periode, als ista daarvan minstens 90%
     (`period_min_pct`) van de dagen gemeten heeft: per resterende dag
     dezelfde dag een jaar eerder, en voor een dag daarvan zonder meting het
     jaarverbruik van die periode × het stookprofiel;
  3. anders geen prognose (`forecast_error` zegt waarom). Het gebouwpatroon
     en de eigen maandcijfers zijn geen terugval meer.
- Marge: √((rest × u × prijs)² + (verwacht × p × prijs)²). `u` is bij de
  vorige periode berekend uit de maanden tot nu tegenover dezelfde maanden
  van de vorige periode (gewogen spreiding, tussen 3 en 25%, minstens 2
  maanden met 20 dagen; anders 15%), bij een ingevuld verbruik 20%. `p` is
  10% zolang de prijs uit de vorige afrekening komt, anders 0. Alles aan te
  passen onder Instellingen; `percentages` in `/api/ista/costs` zegt wat er
  gebruikt is.
- Stand van zaken: betaald voorschot tot nu, verwachte eindkosten,
  verwacht bij- of terugbetalen, advies voor het voorschot.

De prijs per eenheid staat pas vast na de afrekening: hij hangt af van de
energiekosten van het hele complex en van alle eenheden samen. Verbruikt
het complex minder, dan dalen de variabele kosten maar ook het aantal
eenheden; de prijs per eenheid volgt vooral de energieprijs. Daarom is de
schatting expliciet een schatting.

### I6 — Wachtwoord versleuteld bewaren

De hele config van elke plugin-instance en elk plugin-apparaat (daarin het
ista-wachtwoord, maar ook het Tuya-secret en de local keys) wordt versleuteld
opgeslagen: AES-256-GCM met een sleutel in `secret.key` in de data-map (alleen
leesbaar voor de eigenaar, bij de eerste start aangemaakt), als
`enc:v1:<base64>` (`store.UseKey`, `sealConfig`/`openConfig`). De store hoeft
zo niet te weten welke velden geheim zijn. Bestaande onversleutelde configs
worden bij de start omgezet. Een oudere versie van hwlocal kan een
versleutelde config niet lezen (bij teruggaan opnieuw invullen). Dit beschermt de database en de `.bak`-kopieën (bijvoorbeeld in een
back-up); wie bij de hele data-map kan, kan ook ontsleutelen. Zonder
`secret.key` (bijv. een database naar een andere machine gekopieerd) moet
het wachtwoord opnieuw worden ingevuld.

### I7 — Weken

Een week loopt van maandag t/m zondag (ISO-week). De weektabel toont per
week de stand aan het eind van zondag en het verbruik in die week; een week
die over de grens van een afrekenperiode loopt, wordt daar gesplitst.

## Gebouwd

- `internal/ista`: `client.go` (inloggen, de vier calls, opnieuw inloggen bij
  401, `MonthValues` in stukken), `sync.go` (`Service`: historie, dagelijks,
  wekelijks, status), `usage.go` (verbruik per dag/week/maand/periode,
  weektabel), `costs.go` (contracten, prijzen, prognose, voorschot,
  controle), `provider.go` (plugin), `sim.go` (nep-ista met vier radiatoren).
- `internal/store/ista.go` en migratie 18; `internal/store/secret.go`
  (versleutelde plugin-config, I6).
- `internal/server/ista.go`; `web/static/heating.js`.
- Tests tegen `ista.Sim`: inloggen, verkeerd wachtwoord, verlopen sessie,
  historie en dagelijks bijwerken, verbruik telt op tot de stand aan het eind
  van een periode, weken, kosten met de echte afrekening als controle, een
  periode met intrek halverwege, voorschotbetalingen, de API.
- Niet getest: het echte mijn.ista.nl vanuit de gateway (de client volgt de
  probe die wel tegen het echte account werkte), en diensten naast
  verwarming (warm water) of meerdere adressen in één account.

## Beantwoorde vragen (2026-11-11)

- Opbouw van de jaarafrekening: zie I5.
- Week: maandag t/m zondag (I7).
- Wachtwoord bewaren: ja, versleuteld (I6).
- Branch: `feat/regelen` is gemerged (migratie 17); dit wordt gebouwd op
  `feat/ista` vanaf `main`, nieuwe migraties vanaf 18.
