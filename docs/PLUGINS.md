# Plugins: extra apparaten naast HomeWizard

Doel: naast de HomeWizard-energieapparaten ook andere lokale (en soms cloud-)
apparaten kunnen uitlezen — te beginnen met de airco (Mitsubishi Heavy
Industries, WF-RAC-wifimodule) en later de verwarming (Tado). Elke integratie
is een **plugin**: onder Instellingen voeg je een plugin toe, krijgt daarbij
een plugin-eigen configuratiescherm, en voegt daarbinnen apparaten toe (of laat
ze scannen). Op het Live-scherm komt er onderaan een tegel per plugin-apparaat
met de waarden die eruit komen.

Status: **in aanbouw** op branch `feat/climate-mhi`. Eerste stap is het
framework + de MHI-plugin, read-only. Dit document legt het ontwerp en de
genomen beslissingen vast.

## Uitgangspunten

- Read-only als startpunt. Besturen (schrijven) kan later, per plugin, en
  alleen met expliciete bevestiging — zie "MHI: schrijven en accounts".
- De HomeWizard-energiekern blijft ongewijzigd. Plugins staan **ernaast**, niet
  eroverheen (zie beslissing D2).
- Alles blijft lokaal waar dat kan. Eén uitzondering is bekend: Tado heeft geen
  lokale API en moet via de cloud (beslissing D6).

## Beslissingen

### D1 — "Plugin" = meegecompileerd provider-type, geen los bestand

`hwlocal` is één statisch gelinkte Go-binary, draait als Windows-service en
wordt gecross-compileerd (linux amd64/arm64/arm7, windows, darwin). Go's
`plugin`-pakket (losse `.so`-bestanden dynamisch inladen) werkt alleen op Linux
en breekt de service en de cross-builds. Een plugin is daarom een **intern
Go-pakket dat zich bij start registreert** in een registry. Voor de gebruiker
verandert dat niets: het toevoegen, configureren, scannen en de tegels werken
precies zoals hierboven beschreven. Een nieuw plugin-type toevoegen is één klein
zelfregistrerend pakket, zonder wijzigingen elders.

### D2 — Plugins naast de HomeWizard-kern, niet erin

De energiekern (`gateway` → `homewizard.Client` → `history.Recorder`) zit vol
energie-specifieke logica: cumulatieve tellers, kwartierwaarden, netkwaliteit,
kosten. Een airco of thermostaat (temperatuur, modus, setpoint) past daar niet
in. Het plugin-framework is daarom een eigen subsysteem (`internal/plugin`) met
een eigen, generiek datamodel. HomeWizard wordt **niet** door de generieke laag
geperst; dat houdt het risico laag en het werkende energiedeel intact. In de
Plugins-UI mag HomeWizard eventueel als vast, niet-verwijderbaar item getoond
worden voor de herkenbaarheid, maar de code blijft gescheiden.

### D3 — Generieke meting: een lijst metrics

Een plugin levert per apparaat een `Reading` met een lijst `Metric`s
(`label`, `value`, `unit`, `kind`), plus een paar vaste velden (apparaat-id,
naam, online). Zo kan de Live-tegel renderen wat een plugin ook maar oplevert
(temperatuur, setpoint, modus, ventilatorstand, vocht, foutcode) zonder dat de
kern de details hoeft te kennen.

### D4 — Opslag in twee tabellen

Nieuwe SQLite-migraties (volgens de bestaande conventie: toevoegen aan de
geordende slice in `store.go`, nooit een bestaande wijzigen):

- `plugins` — geconfigureerde plugin-instanties: `id`, `type`, `config` (JSON),
  `enabled`, tijdstempels.
- `plugin_devices` — apparaten binnen een instantie: `id`, `plugin_id`, `name`,
  `address`, `config` (JSON), `enabled`, tijdstempels.

### D5 — UX: Instellingen configureert, Live toont

- **Instellingen → Plugins**: plugin toevoegen → type kiezen → een
  configuratiescherm dat de PWA **automatisch uit het schema** van de plugin
  tekent → apparaten toevoegen of laten scannen.
- **Live**: onderaan een rij tegels, één per plugin-apparaat, met de metrics.

De metingen lopen over de bestaande SSE-stream (`/api/stream`) als een nieuw
event-type, zodat de PWA realtime bijwerkt zoals bij de energieapparaten.

### D6 — Tado kan niet lokaal; MHI wel

Tado heeft geen gedocumenteerde lokale API: de Internet Bridge praat met de
Tado-cloud (OAuth op `my.tado.com`). De Tado-plugin wordt daarom een
cloud-plugin: OAuth-koppeling, refresh-token in de data-map (zoals het
ENTSO-E-token), en **traag** pollen (Tado beperkt verbruikers tot ~100
API-calls per dag). Dit deel is dus bewust níet LAN-only. MHI daarentegen is
volledig lokaal en read-only zonder authenticatie uit te lezen.

### D7 — MHI eerst, read-only

De MHI-plugin leest alleen (geen schrijven). Lezen mag vanaf elke client op het
LAN; schrijven vereist registratie als account op de module (zie onder) en
wordt later pas gebouwd, met expliciete bevestiging.

### D8 — Bouwvolgorde

1. Framework + registry + opslag + MHI-plugin (read-only) + Settings-UI +
   Live-tegels — in één PR.
2. Tado-plugin (cloud).
3. Alerts op plugin-metrics (aanhaken op de bestaande alerts-engine) —
   gebouwd, zie "Regels op plugin-apparaten".

### D9 — Branch vanaf `main`

`feat/climate-mhi` is afgetakt van `main`, niet van `feat/alerts`, zodat de
plugin-PR onafhankelijk van de nog niet gemergde alerts-PR te reviewen is.

## Architectuur

```
internal/plugin/
  registry.go   alle beschikbare plugin-typen (compile-time self-register)
  provider.go   Provider (het type) + Instance (geconfigureerd)
  reading.go    generieke Reading + Metric
  manager.go    pollt elk apparaat, publiceert naar de bestaande gateway.Hub
  mhi/          eerste plugin: read-only WF-RAC
  tado/         later: cloud
```

- **Provider** (het type) declareert: `Type()` (bijv. `"mhi"`), `Name()`,
  `ConfigSchema()` (velddefinities waaruit de PWA het configscherm tekent),
  capability-vlaggen (handmatig toevoegen? scannen?), en `New(config)` dat een
  `Instance` maakt.
- **Instance** (geconfigureerde plugin) kan `Scan(ctx)` (gevonden apparaten,
  optioneel) en `Poll(ctx)` (read-only metingen).
- **Manager** laadt instanties en apparaten uit de store, pollt elk ingeschakeld
  apparaat op het eigen interval, en publiceert `Reading`s naar `gateway.Hub`.

### HTTP-API

- `GET /api/plugins` — beschikbare typen (`types`, met configuratieschema) en
  de geconfigureerde instanties met hun apparaten en laatste metingen
  (`instances`).
- `POST /api/plugins` — instantie toevoegen (`{type, name, config}`).
- `PATCH /api/plugins/{id}` — naam, `enabled` of config wijzigen (lege geheime
  velden houden hun bestaande waarde).
- `DELETE /api/plugins/{id}` — instantie en zijn apparaten verwijderen.
- `POST /api/plugins/{id}/scan` — apparaten laten scannen (`{candidates}`).
- `POST /api/plugins/{id}/devices` — apparaat toevoegen (`{address, name, config}`).
- `PATCH /api/plugins/devices/{id}` — apparaat wijzigen (`{name, enabled,
  control}`). `control: false` zet de bediening voor alleen dit apparaat uit
  (`_control: "false"` in de apparaat-config); de schakelaar van de instantie
  gaat voor. De apparaatweergave heeft `can_control` (bediening toegestaan
  voor de instantie) en `control` (aan voor dit apparaat).
- `DELETE /api/plugins/devices/{id}` — apparaat verwijderen.
- `PUT /api/plugins/devices/{id}/meter` — een tussenstekker of kWh-meter
  koppelen (`{meter: "<apparaat-id>"}`, leeg ontkoppelt). Eén meter per
  apparaat (unieke index op `plugin_device_meters.plugin_device_id`); één
  meter mag wel meerdere apparaten meten. Alleen voor typen met
  `can_link_meter` in `ProviderInfo` (MHI, Tuya; niet tado). Komt terug als
  `meter` en `can_link_meter` in de apparaatweergave (API en stream).
  `can_regulate` in `ProviderInfo` (MHI, Tuya; niet tado) komt op dezelfde
  manier terug: alleen zulke apparaten kan een automatisering *regelen*
  (actie `regulate`, een temperatuur in stappen bijstellen op een andere
  meetwaarde).
- `PATCH /api/plugins/{id}` met `{control: true|false}` — bediening toestaan
  (alleen voor typen met `can_control`; staat standaard uit). Opgeslagen als
  `_control` in de instance-config, nooit via het formulier.
- `POST /api/plugins/devices/{id}/control` — `{key, value}`: één bediening
  (403 als bediening uit staat, 400 bij een onbekende bediening of waarde).
  Antwoordt `202` met de status van de opdracht; `internal/control` stuurt
  hem op de achtergrond, kijkt of het apparaat de stand overneemt en
  herhaalt hem zo nodig (HANDLEIDING, *Bedienen*), met de voortgang als
  stream-event `control`. Na elke poging wordt de instantie meteen opnieuw
  uitgelezen. Automatiseringen gebruiken dezelfde weg.
- `GET /api/plugins/tabs` (`{tabs, max}`), `POST /api/plugins/tabs` (`{name}`,
  hooguit 3), `PATCH`/`DELETE /api/plugins/tabs/{id}` — eigen tabbladen
  (tabel `plugin_tabs`); `PUT /api/plugins/devices/{id}/tab` (`{tab_id}`, 0 =
  Klimaat) zet een apparaat op een tabblad (`plugin_devices.tab_id`, `ON
  DELETE SET NULL`). De tabbladen staan in de snapshot (`plugin_tabs`) en
  komen als `plugin_tabs`-event bij een wijziging.
- `GET /api/plugins/devices/{id}/history?metric=&from=&to=&points=` — één
  metric door de tijd (`{step_s, points: [{t, avg, min, max}]}`, standaard de
  laatste 24 uur). Getallen en ja/nee (als 0/1) uit elke online meting staan
  in `plugin_samples`, opgeschoond na `HWLOCAL_RETENTION_DAYS`.
- Metingen lopen over de bestaande `GET /api/stream`: het `snapshot`-event
  bevat een `plugins`-lijst, daarna komen `plugin`-events (één apparaat) en
  `plugins`-events (de hele lijst na een wijziging).

## MHI-plugin (Mitsubishi Heavy Industries, WF-RAC)

Onderzocht en read-only bevestigd op 2026-11-07 tegen een echte unit
(`WF-RAC-HTTPS`, wifi-fw 025, mcu-fw 200).

### Transport

- HTTPS op poort **51443**, self-signed certificaat (certificaatcontrole uit,
  net als bij een onbekende LAN-apparaat-CA). Platte HTTP geeft geen antwoord.
- JSON POST naar `/beaver/command/<command>` ("beaver"-protocol). Body bevat
  o.a. `apiVer`, `command`, `deviceId`, `operatorId`, `timestamp`, en voor
  sommige commando's `contents.airconId`.

### Read-only commando's

- **`getDeviceInfo`** → `airconId`/`macAddress`, `apMode`.
- **`getAirconStat`** → de volledige toestand. Losse velden: `firmType`,
  `wireless.firmVer`, `mcu.firmVer`, `timezone`, `ledStat`, `autoHeating`,
  `numOfAccount`, `remoteList` (geregistreerde client-UUID's), `updatedBy`,
  `expires` (deadline schrijflock), en `airconStat`: een base64-blob met de
  echte toestand.

Lezen is niet account-gebonden: een niet-geregistreerde client mag uitlezen.

### De `airconStat`-blob decoderen

De blob is base64 → bytes, opgesplitst in een commando-helft en een
status-echo-helft, elk met een CRC16-CCITT (polynoom 0x1021, init 0xFFFF)
achteraan. Decoderen van de toestand (bevestigd tegen de echte unit):

1. bytes = base64-decode; maak een signed-view: `b>127 ? b-256 : b`.
2. `start = signed[18]*4 + 21`; `content = signed[start : start+18]`.
3. Uit `content`:
   - aan/uit: `content[2] & 3 == 1`
   - modus: `60 & content[2]` → 8/16/12/4 → koelen/verwarmen/ventileren/
     ontvochtigen, anders auto
   - setpoint °C: `content[4] / 2`
   - ventilator: `15 & content[3]` → 7/0/1/2/6 → auto/stil/laag/midden/hoog
   - lamellen verticaal: `content[2] & 192 == 64` → auto, anders uit
     `240 & content[3]`
   - foutcode: `content[6]` (`00` = geen fout)
4. Na `content` volgen 4-byte segmenten (vanaf `signed[start+19 : -2]`):
   buitentemp `tag -128, sub 16`, binnentemp `tag -128, sub 32`, elk via de
   fabriekstabellen (NTC-thermistortabel, 256 ingangen). Service-data
   (compressor-Hz, stroom, warmtewisselaar-temps, vocht, ontdooistatus) zit
   níet in een gewone `getAirconStat`; die vereist een aparte aanvraag via de
   schrijf-endpoint (zie onder) en valt dus buiten read-only.

De decoder wordt in Go herbouwd in `internal/plugin/mhi` met een test tegen een
echte blob. Referentie-implementatie (algoritme en thermistortabellen, niet
uitgevoerd maar als bron): de open-source library `blues-sechseck/pywfrac`.

### Scannen

Een subnet-scan op poort 51443 vindt WF-RAC-modules; `getDeviceInfo` bevestigt
dat het er een is en geeft het `airconId`.

### Bediening

De optionele `Controller`-interface (`Controls(dev)`, `Set(ctx, dev, key,
value)`) staat naast `Instance`; alleen typen die hem implementeren kunnen
bediend worden. Een `Control` is een `switch` (bool), `setpoint`
(min/max/step), `select` (opties) of `action` (knop); `metric` wijst de
metric met de huidige waarde aan. De manager controleert de waarde
(`CheckValue`: bereik, afronden op de stap, bekende optie) voordat de
provider hem ziet, en geeft `controls` alleen mee in de apparaatweergave als
de gebruiker bediening voor de instantie heeft toegestaan. De PWA bouwt de
knoppen generiek, zonder plugin-specifieke code.

- **MHI**: aan/uit, modus, temperatuur (16-31 °C), ventilator, flap
  boven/onder (zwaaien of 4 standen), flap links/rechts (zwaaien, 5 standen,
  breed of smal) en 3D auto. Een gekozen flapstand zet 3D auto uit, anders
  stuurt de unit de flappen zelf weer bij. `Set` leest
  de toestand, wijzigt één veld en schrijft de volledige toestand terug
  (`encode.go`, tegenhanger van `decode.go`; de CRC van de commando-helft is
  getest tegen een echt frame). Bij `result 2` registreert de gateway zich
  eenmalig met `updateAccountInfo` (`accountId` = `hwlocal`) en probeert
  opnieuw. `result 1/11/12` = bezet (schrijflock van een andere app). Een
  optionele watchdog (`max_on_hours`) zet een airco die de app aanzette na
  die tijd weer uit. `hwsim` simuleert een unit op poort 18551.
- **tado**: temperatuur (5-25 °C) en een keuze **Stand** (metric `mode`):
  Schema, Handmatig (houdt de laatst gelezen temperatuur vast, anders 20 °C)
  of Uit; bij warm water Schema, Aan of Uit. De oude acties `off` en
  `schedule` zijn verborgen (`hidden`) maar werken nog, voor regels die ze
  al gebruiken. Altijd een overlay
  met `TIMER` (`overlay_minutes`, standaard 120); de instantie onthoudt haar
  eigen overlays en verlengt ze bij het pollen als ze vóór de volgende poll
  zouden aflopen. Een overlay die weg is of elders gewijzigd, wordt
  losgelaten. Valt de gateway uit, dan valt tado terug op het schema.
- **Tuya**: wat het device-model (bij het scannen opgehaald, zie hieronder)
  schrijfbaar noemt (`accessMode` `rw`): bool → schakelaar, value → setpoint
  (min/max/stap gedeeld door 10^scale), enum → keuze. Kinderslot,
  temperatuurcorrectie en fabrieksreset worden nooit bediend, alleen getoond.
  Schrijven gaat lokaal met `CONTROL` (0x07, 3.3) of `CONTROL_NEW` (0x0d,
  3.4/3.5) via dezelfde sessie als het uitlezen; het apparaat antwoordt met
  een returncode (niet 0 = geweigerd). Handmatig toegevoegde apparaten (geen
  model) hebben geen bediening. Een radiator heeft geen terugvaltimer zoals
  tado: dezelfde watchdog als bij MHI (`max_on_hours`) zet een apparaat dat
  de app aanzette of hoger zette na die tijd uit. `hwsim` simuleert een
  radiator (3.4) op 127.0.0.1:6668 met een nep-Tuya-cloud op :18080 (regio
  `http://127.0.0.1:18080`).

### Schrijven en accounts (achtergrond)

- Besturen gaat via **`setAirconStat`** met een compleet, zelf opgebouwd
  toestandsframe (commando-helft + status-echo + CRC per helft). De
  commando-helft-CRC is geverifieerd tegen de echte unit.
- Schrijven vereist dat de client **geregistreerd** is: een ongeregistreerde
  `setAirconStat` wordt geweigerd met `result:2`. Registreren met
  **`updateAccountInfo`** (`accountId` = eigen `operatorId`) voegt een blijvend
  account toe aan de module. De module houdt **maximaal 4** accounts bij
  (`remoteList`); één is doorgaans de Smart M-Air-app.
- Opruimen kan met **`deleteAccountInfo`** (`accountId` = de client-UUID uit
  `remoteList`), één HTTP-call, `result:0` = gelukt. Let op: de app-UUID
  verwijderen ontkoppelt de telefoon-app.
- Er is geen wachtwoord; wie op het LAN zit, kan lezen én (na registratie)
  schrijven. Houd dit in gedachten bij gastennetwerken.

## Tado-plugin (cloud)

Gebouwd. Cloud-only (D6), read-only.

- **Inloggen (eenmalig)**: OAuth2 **device-code flow** op `login.tado.com`
  (publieke `client_id`, scope `offline_access`). De gebruiker klikt
  "Verbinden met tado" in het configscherm, opent de getoonde link, logt in bij
  tado en keurt goed. De gateway pollt op de achtergrond het token-endpoint en
  bewaart het **refresh-token** in de data-map (`tado-<instanceid>.json`,
  owner-only). Refresh-tokens roteren; elke refresh overschrijft het bestand.
  Het wachtwoord raakt de gateway nooit.
- **Framework-uitbreiding** die dit mogelijk maakte: de optionele
  `Authenticator`-interface (`StartAuth`/`AuthStatus`) met generieke endpoints
  (`POST`/`GET /api/plugins/{id}/auth`) en een `needs_auth`-vlag; de `Env` die
  providers de data-map geeft; en `BatchPoller` (`PollAll`) zodat één API-call
  alle zones leest.
- **Apparaten**: "Kamers ophalen" (scan) leest `/me` → `/homes/{id}/zones` en
  maakt per **zone (kamer)** een apparaat (`home_id:zone_id`). De bridge en
  andere fysieke apparaten worden bewust niet toegevoegd.
- **Pollen**: `BatchPoller.PollAll` leest `/homes/{id}/zoneStates` in **één
  call** voor alle kamers samen, elke **15 min** (≈96 calls/dag, onder tado's
  gerapporteerde ~100/dag). Per kamer: temperatuur, ingestelde temperatuur
  (als aan), luchtvochtigheid, verwarmingsvermogen %, aan/uit, open raam.
- **Token**: een access-token wordt hergebruikt tot kort voor de vervaltijd en
  anders ververst via het refresh-token.

## Tuya / Smart Life-plugin

Gebouwd. **Lokaal uitlezen en (als je dat toestaat) bedienen.** Een Tuya-
of Smart Life-apparaat praat op het LAN een versleuteld protocol (TCP 6668); de
`local key` is het geheim dat de app-cloud anders beschermt.

- **Koppelen via het Tuya IoT-project (scan)**: in het configscherm vul je de
  **Access ID + Access Secret** van je Tuya IoT-project in (plus de regio). Scan
  logt in op de officiële OpenAPI (`openapi.tuya<regio>.com`, HMAC-SHA256-
  signing), haalt je gekoppelde apparaten **met hun `local_key`** op, en
  lokaliseert ze op het netwerk. Je hoeft geen keys of IP's te typen. De key-
  bron is bewust een losse laag (`cloud.go`); een Smart Life-wachtwoordlogin zou
  er later naast kunnen, maar die vereist niet-gepubliceerde, roterende geheimen
  uit de Smart Life-APK en is daarom niet gekozen.
- **LAN-lokalisatie** (`discovery.go`): eerst luisteren naar Tuya's UDP-
  discovery-broadcasts (geven IP + protocolversie); wat daar niet opduikt
  (bijv. als een firewall inkomende broadcasts blokkeert) wordt gevonden door
  het `/24` op poort 6668 te scannen en elke host te identificeren met de
  handshake per bekende key — een geslaagde handshake bewijst welk apparaat en
  welke versie het is.
- **Protocol** (`protocol.go`): local control voor versie **3.3** (AES-ECB +
  CRC), **3.4** (AES-ECB + HMAC, met sessie-handshake) en **3.5** (AES-GCM,
  0x6699-frame, met sessie-handshake). Alleen een `DP_QUERY` (lezen); de framing
  en sessiesleutel-afleiding zijn puur en getest (`protocol_test.go`).
  `CONTROL`/`CONTROL_NEW` voor de bediening; `sim.go` is de apparaatkant van
  hetzelfde protocol (`SimDevice`), getest per versie in `sim_test.go`.
- **Pollen**: elke **30 s** wordt het apparaat lokaal uitgelezen; de datapoints
  (dps) worden metrics. Bij een scan haalt de plugin ook het **device-model** op
  (`/v2.0/cloud/thing/{id}/model`) en bewaart een dp-id → code/eenheid/schaal-
  schema bij het apparaat (ook type, `accessMode`, bereik en enum-waarden),
  zodat de tegel "Temperatuur 18,1 °C" toont i.p.v. "DP 24 181" en de
  bediening weet wat schrijfbaar is. Handmatig toegevoegde apparaten (zonder
  schema) tonen generieke `DP n`-labels. Opnieuw scannen werkt de config van
  al toegevoegde apparaten bij (nieuwe local key, nieuwer model), zonder dat
  meter, tabblad of historie verloren gaan.
- **Handmatig toevoegen** kan ook: IP, device id, local key en versie in het
  apparaatformulier. Nuttig als fallback of zonder IoT-project.
- **Geheimen**: de Access Secret (instance) en elke `local_key` (apparaat)
  worden opgeslagen maar nooit door de API teruggegeven (`Secret`-velden; de
  views laten `config` weg).
- **Buiten scope**: schedulen op het apparaat zelf.

## Thuisbatterij-plugin (`homewizard_battery`)

`internal/plugin/hwbattery`: de Plug-In Batteries die een HomeWizard P1-meter
(of kWh-meter) aanstuurt via `/api/batteries` (API v2). Geen eigen apparaat
of account: de provider krijgt de `gateway.Manager` (`Provider{Meters: mgr}`,
geregistreerd in `main.go`) en gebruikt de gekoppelde meter en zijn token.
Zo loopt de bediening via dezelfde weg als elk ander apparaat (`control`,
`plugin_set` in regels, tegels) en alleen als de gebruiker bediening
toestaat.

- **Scan**: de P1- en kWh-meters die via API v2 gekoppeld zijn en een
  batterijgroep met batterijen melden; adres = id van de meter.
- **Poll**: `GET /api/batteries` → metrics `stand` (tekst), `power_w`,
  `target_power_w`, `state_of_charge_pct` (gemiddelde van de batterijen die
  de gateway uitleest), `charge_allowed`, `discharge_allowed`,
  `max_consumption_w`, `max_production_w`, `battery_count`.
- **Bediening**: één select `mode` (metric `stand`) met de standen `zero`,
  `charge_only`, `discharge_only`, `standby`, `to_full`, `predictive`. Een
  stand is een combinatie van `mode` en `permissions`; `Set` leest eerst de
  meter en kiest het verzoek dat bij de firmware past (permissies vanaf API
  2.2, `charge_to_full` en `predictive` vanaf 2.3), en beëindigt eerst een
  lopende *volladen* (daarin zijn de permissies alleen-lezen). Na de eerste
  poll worden alleen de standen aangeboden die de firmware kent.
- `hwsim`: de P1 heeft `/api/batteries` op API v2; de stand bepaalt wat de
  gesimuleerde batterij doet (`cmd/hwsim/batteries.go`).

## Regels op plugin-apparaten

Gebouwd (stap 3 van issue 39). De regel-engine (`internal/rules`) krijgt de
plugin-manager als `Engine.Plugins` en evalueert ook plugin-apparaten:

- **Conditie**: `device: "plugin:<id>"`, `metric: "<key>"`. Getallen direct,
  ja/nee als 1/0, `online` uit de meting. Tekst-metrics (zoals de modus) met
  `op: "="` en `text` (hoofdletterongevoelig). Offline of een ontbrekende
  metric = onbekend: de regel gaat niet af en wordt niet gewapend.
- **Actie** `plugin_set`: `{device: "plugin:<id>", key, value}`. Bij opslaan
  wordt gecontroleerd dat het apparaat bediening heeft (toegestaan) en dat
  de waarde past (`plugin.CheckValue`; een setpoint wordt afgerond op de
  stap). Bij afgaan loopt het via `Manager.Set`, dus dezelfde controles en
  hetzelfde log als bediening vanaf de tegel; het resultaat staat in de
  melding en in `last_result`.

## Alerts op plugin-metrics (oorspronkelijk plan)

De alerts-engine (`internal/rules` + `internal/webpush`, gebouwd op
`feat/alerts`) evalueert nu regels tegen `Manager.Views()` van de
energieapparaten en stuurt Web Push of schakelt sockets. Voor plugins is het
plan om dezelfde engine te laten evalueren tegen plugin-metrics, zodat je bijv.
een melding krijgt bij een airco-foutcode, een kamertemperatuur buiten een
bandbreedte, of een Tado-zone die te koud wordt. Push naar een echte telefoon is
nog niet getest (vereist een beveiligde context, bijv. `tailscale serve` op de
gateway-pc).

## Migratienummers en branch-botsingen

De schemaversie (`PRAGMA user_version`) is één teller die door alle branches
gedeeld wordt. Twee branches die elk een migratie toevoegen, claimen allebei
hetzelfde volgnummer. Dat is hier gebeurd: op `feat/climate-mhi` is migratie 9
= `plugins`/`plugin_devices`, op `feat/alerts` is migratie 9 =
`rules`/`push_subscriptions`. Een database die door een alerts-build op versie
9 is gezet heeft dus de alerts-tabellen, maar niet de plugin-tabellen — en
omdat de teller al op 9 staat, draait migratie 9 (plugins) daar nooit meer.
De gateway crasht dan bij de start met `no such table: plugins`.

Vangnet: `store.ensurePluginSchema` draait bij elke `Open`, los van de teller,
en maakt de plugin-tabellen met `CREATE TABLE IF NOT EXISTS`. Zo heelt een DB
die via een andere branch op versie 9 kwam zichzelf, zonder de versie te
wijzigen of data te verliezen. De tabellen kunnen níet uit de migratieslice
worden gehaald (dan zou een DB op versie 9 "nieuwer dan de binary" zijn en
geweigerd worden), dus migratie 9 blíjft plugins; het vangnet staat ernaast.

**Bij het mergen van `feat/alerts`:** hernummer die migratie naar 10 (plugins
is 9 op `main`). Een DB die al via de alerts-build `rules`/`push_subscriptions`
heeft, draait dan migratie 10 opnieuw — maak die daarom idempotent
(`CREATE TABLE IF NOT EXISTS`), net als hierboven, of geef alerts een eigen
`ensure`-vangnet.

## Zie ook

- [HANDLEIDING.md](HANDLEIDING.md) — gebruikersreferentie (configuratie, API, deployment).
- `docs/CONTRACTS.md` — het kosten-/contractenmodel.
