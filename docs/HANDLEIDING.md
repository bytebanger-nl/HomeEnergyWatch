# Home Energy Watch — handleiding

Lokaal energiedashboard: een gateway met een PWA voor realtime inzicht en
historie, volledig binnen je eigen netwerk. Het werkt met HomeWizard
Energy-apparaten via hun lokale API; er gaat niets via de HomeWizard-cloud.

> Home Energy Watch is een onafhankelijk project, niet verbonden aan of
> goedgekeurd door HomeWizard. HomeWizard is een merk van de eigenaar ervan.
> Het programma, de dienst en de mappen heten technisch `hwlocal`.

Een overzicht met schermafbeeldingen staat in de [README](../README.md); elk
scherm stap voor stap in de [gebruikshandleiding](GEBRUIKSHANDLEIDING.md).
Deze handleiding is de naslag: installeren, instellen en hoe het werkt.

- **Dashboard:** netvermogen (afname/teruglevering), per fase, vandaag-totalen,
  gas, en een kaart per apparaat, standaard **elke 2 seconden** bijgewerkt.
  Een kaart toont het verbruik van vandaag, en bij tussenstekkers en
  kWh-meters ook wat dat vandaag tot nu toe kost (afname tegen je
  contractprijs met energiebelasting en btw, zonder vaste kosten, per
  afgerond kwartier). De meterstanden (per tarief, gas, water) zet je per
  apparaat aan onder **Apparaten** (*Meterstanden tonen op het dashboard*).
- **Meldingen en automatisering:** pushmeldingen op elke waarde, en
  automatisch schakelen en bedienen, ook op basis van prijzen.
- **Verbruik en Kosten:** één tabblad met bovenaan de keuzes *Energie* of
  *Water*, het apparaat, de periode (*Vandaag*, *Gisteren*, *7 dagen*,
  *Maand*, *Jaar*, *Contractperiode*, *Alles*, met ‹ › terug en vooruit) en de
  weergave:
  - **Verbruik:** vermogen per minuut (vandaag, gisteren, 7 dagen), stroom
    en gas per kwartier (een dag), dag, maand of jaar. Kwartierwaarden worden
    berekend uit de meterstanden (net als de vroegere Grip op
    Energie-collector), met afname en teruglevering apart;
  - **Kosten:** de kosten per uur (een dag), dag, maand of jaar, met
    daaronder het voorschot, je contracten en *Vergelijken* (zie
    [Kosten en contracten](#kosten-en-contracten));
  - **Net** (P1- en kWh-meter): spanning, stroomsterkte en vermogen per fase
    en de netgebeurtenissen.

  Een **contractperiode** is een contract van begin tot eind; contracten die
  op elkaar aansluiten (een tariefwijziging bij dezelfde leverancier) tellen
  als één periode. Oude links naar Historie en Kosten openen dit tabblad.
- **Netkwaliteit:** spanning, stroom en vermogen per fase per minuut (P1- en
  kWh-meters), en de spanningsdips, -pieken en stroomstoringen die de slimme
  meter telt.
- **Apparaten:** automatisch gevonden via mDNS of toe te voegen op IP-adres;
  hernoemen, koppelen (API v2), Energy Sockets schakelen.

Ondersteund: P1-meter (`HWE-P1`), Energy Socket (`HWE-SKT`), kWh-meters
(`HWE-KWH1/3`, `SDM230/630-wifi`), watermeter (`HWE-WTR`, alleen met
USB-voeding) en Plug-In Battery (`HWE-BAT`).

```
 telefoon / browser ──(HTTP(S), live via server-sent events)──┐
                                                              ▼
                       ┌──────────────── hwlocal (Go, één bestand) ────────────────┐
                       │ PWA · JSON-API · historie (SQLite) · mDNS · Let's Encrypt │
                       └──────┬──────────────────────┬─────────────────────────────┘
              API v1 (HTTP)   │                      │  API v2 (HTTPS + token)
                 P1 · sockets · kWh · water       P1 · kWh · batterij
```

## Snel starten op je ontwikkel-pc

Nodig: [Go 1.26 of nieuwer](https://go.dev/dl/). Verder niets: de PWA is
gewone HTML/CSS/JavaScript en zit in het programma.

### Met gesimuleerde apparaten

PowerShell, vanuit de repo-map, in twee vensters:

```powershell
go run ./cmd/hwsim
```

```powershell
$env:HWLOCAL_DEVICE_CA = "data/hwsim-ca.pem"
$env:HWLOCAL_DISCOVERY = "false"
$env:HWLOCAL_DEVICES = "127.0.0.1:18001,127.0.0.1:18002,127.0.0.1:18003,127.0.0.1:18004,127.0.0.1:18005,127.0.0.1:18444"
go run ./cmd/hwlocal
```

Open <http://localhost:8080>. De simulator heeft een P1-meter, twee sockets,
een kWh-meter (zonnepanelen), een watermeter en een batterij. De batterij
werkt alleen via API v2: klik op **Koppelen**; de simulator "drukt" na 4
seconden zelf op de knop.

Linux/macOS: `HWLOCAL_DEVICE_CA=data/hwsim-ca.pem HWLOCAL_DISCOVERY=false HWLOCAL_DEVICES=... go run ./cmd/hwlocal`.

### Automatisering met airco en tado testen (kamersimulatie)

`hwsim` simuleert ook één kamer met een CV (via een nep-tado) en een
MHI-airco. Zo probeer je een automatisering met wachtstap en regelen uit
voordat hij je echte airco en tado bedient. Starten (Git Bash, vanuit de
repo-map):

```bash
HWSIM_ARGS="-speed 30 -room-start 17" scripts/dev-sim.sh data/kamersim
```

De hele werkwijze staat stap voor stap in
[docs/KAMERSIMULATIE.md](KAMERSIMULATIE.md): starten, tado en de airco
toevoegen, de automatisering *Verwarmen: eerst CV, dan airco* met korte
tijden, het verloop volgen en wat je moet zien.

### Met je echte apparaten

1. Zet in de **HomeWizard Energy-app** per apparaat *Instellingen → Meters →
   (apparaat) → Lokale API* aan.
2. Start de gateway:

   ```powershell
   go run ./cmd/hwlocal
   ```

3. Open <http://localhost:8080> of `http://<ip-van-je-pc>:8080` op je
   telefoon. Apparaten verschijnen binnen ongeveer 10 seconden. Komt er niets,
   kijk dan of je pc en de apparaten in hetzelfde netwerk zitten (mDNS werkt
   niet tussen VLAN's) en voeg ze toe op IP-adres via **Apparaten**.

Windows vraagt bij de eerste start of het programma netwerktoegang mag
hebben: sta *privé-netwerken* toe, anders kan je telefoon de gateway niet
bereiken.

**Koppelen (API v2).** API v1 werkt zonder koppeling en is voor de meeste
apparaten voldoende. De Plug-In Battery heeft alleen API v2: klik op
**Koppelen** en druk binnen 90 seconden op de knop van het apparaat (bij de
kWh-meter 1 tot 3 seconden). De gateway controleert het HTTPS-certificaat
van het apparaat tegen de CA van HomeWizard.

### Tests

```powershell
go test ./...
```

## Bestaande historie importeren

Onder **Instellingen → Historie importeren** voeg je de CSV-exports uit de
HomeWizard Energy-app toe (resolutie 15 minuten). Ondersteund:

| Export | Kolommen | Komt bij |
|---|---|---|
| P1 stroom | `time,Import T1 kWh,Import T2 kWh,Export T1 kWh,Export T2 kWh,L1 max W,…` | P1-meter |
| P1 gas | `time,Total gas used` (m³) | P1-meter |
| Watermeter | `time,water usage dl` (deciliter) | watermeter |
| Energy Socket / kWh-meter | `time,Import kWh,Export kWh` | de socket of kWh-meter met de passende tellerstand |

Een socket- of kWh-meterexport zegt niet van welk apparaat hij komt. De
gateway kiest het apparaat waarvan de huidige tellerstand gelijk is aan of
net boven de laatste stand in het bestand ligt. Is dat niet eenduidig (het
apparaat is offline, of de export is oud), dan kies je het apparaat zelf in
het overzicht; dat kan ook om de automatische keuze te wijzigen.

Je ziet eerst een overzicht (periode, nieuwe kwartieren, al aanwezig,
onderbrekingen); pas na **Importeren** wordt er iets opgeslagen.

- **Alleen aanvullen:** wat er al is (gemeten of eerder geïmporteerd) wordt
  nooit overschreven; hetzelfde bestand opnieuw importeren voegt niets toe.
  Een P1-stroomexport brengt ook mee welk deel in dal (T1) viel. Kwartieren
  die de gateway mat vóórdat hij dat bijhield, krijgen dat alsnog als je de
  export opnieuw importeert (alleen bij een gelijk totaal); zo rekent een
  vast contract ook oudere feestdagen als dal.
  Een volledig gemeten kwartier van de gateway gaat wel vóór een
  geïmporteerde waarde; een gedeeltelijk gemeten kwartier (bijvoorbeeld
  omdat de gateway halverwege startte) laat de import staan.
  Andersom vervangt een import een kwartier dat de gateway maar deels heeft
  gemeten (of waarin een teller terugliep): dat is een schatting, de export
  heeft de echte waarde. Het overzicht toont die als "vervangen".
- **Niets geschat:** alleen twee standen precies 15 minuten na elkaar geven
  een kwartier. Verbruik tijdens een onderbreking staat in het overzicht als
  *niet toe te rekenen*.
- **Wintertijd:** het dubbele uur (bijv. 25 oktober 02:00–02:45) wordt op
  volgorde opgelost: eerst zomertijd, dan wintertijd. Bevat een bestand dat uur
  maar één keer (zoals de waterexport), dan ontstaat daar een gat.
- **Watermeter-uitval:** standen van precies 0 tussen echte standen zijn
  meetuitval, geen nieuwe meter; ze tellen als onderbreking.
- Voor geïmporteerde dagen toont de vermogensgrafiek het gemiddelde per
  kwartier (er is geen minuutdata).

Via de API: `POST /api/import?dry_run=true` met het CSV-bestand als body
(optioneel `device=<id>`).

## Dynamische prijzen (stroom en gas)

De gateway haalt de day-ahead-stroomprijzen voor Nederland op (per kwartier
sinds 1 oktober 2025, daarvoor per uur) en de gasprijs per gasdag (06:00 tot
06:00). Op het **Dashboard** zie je de stroom- en gasprijs nu en een grafiek per
kwartier voor vandaag en morgen (morgen zodra de prijzen bekend zijn, voor
stroom ± 13:00), met het huidige kwartier in oranje. Gas heeft één prijs per
gasdag: tot 06:00 zie je dus die van de dag ervoor. Worden beide getoond, dan kies je met **Stroom** / **Gas** welke
grafiek. Heb je onder **Verbruik en Kosten** een contract ingevoerd, dan is dat de prijs
die je betaalt (marktprijs met btw, opslag en energiebelasting); anders de
kale marktprijs. Een onderdeel met een vast contract (bij een gesplitst
contract per onderdeel) laat het dashboard weg: de marktprijs zegt dan niets
over wat je betaalt. Het goedkoopste uur staat er alleen bij een dynamisch
stroomcontract.
Nederland zit in de Europese marktkoppeling, dus EnergyZero, Nord Pool, EPEX
en ENTSO-E geven exact dezelfde NL-stroomprijs.

| Bron | Voor | Nodig | Opmerking |
|---|---|---|---|
| `energyzero` (standaard eerst) | stroom en gas | niets | publieke API van EnergyZero. Opgeslagen wordt de kale marktprijs (`base`): zonder btw, inkoopvergoeding en energiebelasting. Eén verzoek geeft ook de dag ervoor en erna. |
| `nordpool` | stroom | niets | onofficiële API achter de Nord Pool-website; kan zonder aankondiging veranderen, en de voorwaarden van Nord Pool dekken geautomatiseerd gebruik niet. Voor persoonlijk gebruik. |
| `entsoe` | stroom | token | officieel en gratis: registreer op transparency.entsoe.eu en vraag via de helpdesk API-toegang aan. Zonder token wordt de bron overgeslagen. |

Het ENTSO-E-token stel je in via de app (**Instellingen → Prijsbronnen**), in de
Windows-installer, of met `ENTSOE_API_TOKEN`. De app en de installer bewaren
het in `entsoe-token.txt` in de datamap; het wordt direct gebruikt en nooit
teruggestuurd naar de app. Plak je de hele regel `ENTSOE_API_TOKEN=...` (ook
met aanhalingstekens of `set`/`export` ervoor), dan wordt alleen het token
gebruikt; dat geldt ook voor een met de hand bewerkt `entsoe-token.txt`. Staat
`ENTSOE_API_TOKEN` ingesteld, dan gaat die voor en kan het token in de app niet
worden gewijzigd.

Stroom staat in de database in EUR/MWh (markt `NL-DA`), gas in EUR/m³ (markt
`NL-GAS`, één periode per gasdag).

**Alleen ophalen wat ontbreekt.** Bij de start en daarna één keer per dag
controleert de gateway elke dag vanaf `HWLOCAL_PRICE_BACKFILL_FROM`
(standaard 1 januari 2026) tot gisteren en haalt alleen dagen op die niet
compleet in de database staan. De eerste keer kost dat ruim 100 verzoeken
per soort (een paar minuten), daarna niets meer. Verder haalt hij elk
kwartier vandaag op en na 12:45 morgen.

**Testen zonder de gateway te starten:**

```powershell
go run ./cmd/hwlocal prices -date 2026-11-04
go run ./cmd/hwlocal prices -date 2026-11-04 -gas
```

```powershell
$env:ENTSOE_API_TOKEN = "<token>"
go run ./cmd/hwlocal prices -date 2026-11-04 -sources entsoe
```

Dit toont per bron het aantal perioden, het gemiddelde en de laagste en
hoogste prijs. Een dag met zomer- naar wintertijd heeft 100 kwartieren, de
omgekeerde 92; een gasdag duurt dan 25 of 23 uur. In de draaiende gateway kan
ook `POST /api/prices/fetch?date=JJJJ-MM-DD` (gas: `&energy=gas`).

De simulator heeft een nep-Nord Pool en nep-EnergyZero op poort 18080:
`$env:HWLOCAL_NORDPOOL_URL = "http://127.0.0.1:18080"` en
`$env:HWLOCAL_ENERGYZERO_URL = "http://127.0.0.1:18080"`.

## Berichten en storingen

Zijn er ongelezen berichten, dan staat rechtsboven in de kopregel een
belletje met het aantal. Een klik opent **Berichten** (`#/berichten`):

- **Problemen**: wat er misging en aandacht vraagt. Dat is een rapport dat
  niet gemaild kon worden, een mislukte back-up, een opdracht van een
  automatisering die na alle pogingen niet lukte, ista die het inloggen
  weigert of een plugin die niet geladen kon worden. Gebeurt hetzelfde weer
  terwijl het bericht nog ongelezen is, dan telt het op hetzelfde bericht
  (*2× · laatst … · eerst …*).
- **Storingen van apparaten**: per apparaat (HomeWizard en plugins) het
  aantal storingen, de duur samen, hoeveel er ongelezen zijn en of het nu
  niet bereikbaar is. Klik op een apparaat om alleen zijn storingen te zien.

**Alles als gelezen markeren** bevestigt wat op de pagina staat; wat daarna
binnenkomt blijft ongelezen. Gelezen is voor de hele gateway, dus op elk
apparaat waarmee je de app opent hetzelfde. Prijzen die (nog) niet opgehaald
zijn en ista-gegevens die nog niet compleet zijn, zijn geen berichten: de
gateway probeert die vanzelf opnieuw.

Bij elk apparaat (onder **Apparaten**, op de kaarten van plugin-apparaten en
onder **Instellingen → Plugins**) staat daarnaast hoe vaak het de afgelopen
30 dagen niet bereikbaar was en hoe lang samen. Klik erop voor een lijst met
per storing het begin, het einde, de duur en de foutmelding.

- Een apparaat telt als niet bereikbaar na drie mislukte pogingen achter
  elkaar; de storing begint bij de eerste daarvan en eindigt zodra het weer
  antwoordt. Eén gemiste meting telt niet.
- Een stop of herstart van de gateway telt niet. Een storing die nog liep
  toen de gateway stopte, eindigt op dat moment (*gateway stopte*; de duur is
  dan "of langer"). Stopte de gateway onverwacht (crash, stroomstoring), dan
  eindigt zo'n storing bij de laatste minuut waarop de gateway nog draaide. Na een start telt een apparaat dat nog niet antwoordt
  pas na twee minuten, tenzij het sinds de start al bereikbaar was.
- Een uitgeschakeld apparaat wordt niet uitgelezen; een lopende storing
  eindigt dan. Bij verwijderen verdwijnen de storingen van het apparaat.

## Kosten en contracten

Onder **Verbruik en Kosten**, weergave **Kosten**, zie je wat stroom en gas
(en water, zie hieronder) kostten per uur (vandaag, gisteren), per dag (7
dagen, maand), per maand (jaar, contractperiode) of per jaar (alles),
berekend uit de kwartierhistorie, de
opgeslagen marktprijzen en je contracten. Geïmporteerde historie telt
gewoon mee.

**Contracten** voer je in met een begindatum (elke dag, bijvoorbeeld
7 september) en optioneel een einddatum; zonder einddatum loopt een contract
tot het volgende begint. Contracten mogen elkaar niet overlappen; een dag
zonder contract wordt gemeld, niet als 0 geteld. **Nieuwe periode** kopieert
een contract met een nieuwe begindatum, voor een tariefwijziging bij
dezelfde leverancier. Zo'n contract staat aangevinkt als *vervolg op het
vorige contract*: de contracten van één leverancier vormen dan samen één
afrekenjaar en één voorschot, hoeveel tariefwijzigingen er ook zijn. Een
contract via **Contract toevoegen** (bijvoorbeeld bij een andere leverancier)
begint een eigen afrekenjaar. Alle bedragen zijn inclusief btw:

| | Dynamisch | Vast/variabel |
|---|---|---|
| Stroom | marktprijs × (1 + btw) + inkoopopslag (ct/kWh) | tarief, of normaal + dal (dal: zoals de P1-meter het telt, dus ook op feestdagen; zonder die gegevens doordeweeks 23–7 uur en weekend) |
| Gas | gasdagprijs × (1 + btw) + inkoopopslag (ct/m³) | tarief (ct/m³) |
| Overheid | energiebelasting stroom (ct/kWh) en gas (ct/m³) | idem |
| Teruglevering | marktprijs (t/m 2026 × (1 + btw), vanaf 2027 kaal en niet onder 0) + vergoeding of − kosten (ct/kWh) | terugleververgoeding (ct/kWh) |
| Vaste kosten | posten in euro per maand (leveringskosten, netbeheer, vermindering energiebelasting als negatief bedrag) | idem |

Vaste kosten tellen per dag mee als maandbedrag × 12 / 365 (366 in een
schrikkeljaar), vanaf de begindatum; vandaag telt naar rato tot nu.

**Stroom en gas apart.** Heb je stroom en gas bij verschillende leveranciers
of met een verschillend soort contract (bijvoorbeeld dynamische stroom en vast
gas), vink dan bij het contract *Stroom en gas apart* aan. Je kiest dan per
onderdeel dynamisch of vast/variabel, vult per onderdeel een voorschot in en
geeft bij elke vaste kostenpost aan of hij bij stroom of gas hoort (de
vermindering energiebelasting hoort bij stroom). Zet beide leveranciers in de
naam, bijvoorbeeld *Tibber / Vattenfall*. Beide onderdelen delen de datums,
het afrekenjaar, de btw en het verwachte jaarverbruik; begint of eindigt het
gascontract op een andere datum, maak dan een apart contract. Zonder vinkje
gelden één soort en één voorschot voor stroom en gas samen.

**Salderen (tot en met 2026)** gaat per kalenderjaar: teruggeleverde kWh
worden weggestreept tegen de afname van hetzelfde jaar, op het moment dat
er genoeg afname tegenover staat. Bij een dynamisch contract krijg je over
die kWh de energiebelasting terug (de marktprijs krijg je al per kwartier);
bij een vast contract is een gesaldeerde kWh het volle tarief plus
energiebelasting waard in plaats van de terugleververgoeding. Vanaf 2027 is
er geen saldering meer.

**Teruglevering vanaf 2027.** Je krijgt voor alle teruggeleverde stroom een
vergoeding van je leverancier. Bij een dynamisch contract is dat de kale
marktprijs: particulieren vallen vrijwel altijd onder de
kleineondernemersregeling en krijgen geen btw. Die vergoeding mag niet
negatief zijn; terugleverkosten vul je in als negatieve vergoeding per kWh.
Tot en met 2026 rekent hwlocal de marktprijs voor teruglevering met btw,
zoals voorheen. Per contract kun je dit onder **Btw** altijd met of altijd
zonder btw laten rekenen.

**Voorschot.** Betaal je een vast maandbedrag aan je leverancier, vink dan
bij het contract *Ik betaal een maandelijks voorschot* aan en vul het bedrag
in, met als datum de dag waarop je **betaalt**. Elke regel is een betaling op
die datum en daarna elke maand op dezelfde dag het volle bedrag (de 31e wordt
in een kortere maand de laatste dag), niet naar rato per dag. Een regel met een
latere datum neemt het over: vanaf die datum geldt de eerdere niet meer. Zo
betaal je met € 150,57 vanaf 14 april en € 126 vanaf 22 april één keer
€ 150,57 en daarna elke 22e € 126. Zonder vinkje (bijvoorbeeld bij Tibber)
worden de kosten achteraf berekend en is er geen voorschotkaart.

Bij **Kosten** zie je dan per afrekenperiode (het contractjaar vanaf de
begindatum van het eerste contract bij die leverancier, of tot de einddatum
of het volgende contract van een andere leverancier). Bij een tariefwijziging
loopt het voorschot door tot de eerste regel van het nieuwe contract; *Nieuwe
periode* zet die op je volgende betaaldag:

- wat je tot nu toe hebt betaald, wat het heeft gekost en hoe je ervoor
  staat;
- wat je bij de afrekening verwacht terug te krijgen of bij te betalen, met
  een bandbreedte;
- welk maandbedrag je over de resterende betalingen nodig hebt om op nul uit
  te komen;
- naast de tegels de *verwachte vaste kosten* en de *verwachte
  verbruikskosten* van de hele afrekenperiode (alle tariefperiodes samen;
  verbruik is gemeten plus prognose, min teruglevering). Ook bij water en
  verwarming.

Bij *stroom en gas apart* staat dit per onderdeel op de kaart: het
stroomvoorschot tegenover de stroomkosten (met teruglevering, saldering en
de vaste kosten van stroom), het gasvoorschot tegenover de gaskosten en de
vaste kosten van gas.

De prognose rekent met dezelfde regels als de kosten (tarieven, btw,
salderen tot 2027). Waar je metingen hebt, gelden die; een dag waarop een
paar kwartieren ontbreken (minstens 92% gemeten) telt als gemeten, en de
ontbrekende kwartieren krijgen het gemiddelde van die dag. Voor de rest van
de periode, en voor dagen in het verleden zonder metingen (bijvoorbeeld van
voordat de gateway draaide; de kaart meldt hoeveel), komt het verbruik per
onderdeel (afname, teruglevering, gas) van:

1. het **verwacht jaarverbruik** dat je bij het contract invult, verdeeld
   met een seizoensprofiel per maand. Een ingevulde waarde gaat altijd voor;
   de kaart zet het verbruik van de vorige periode ernaast;
2. anders de **vorige periode**: de 12 maanden vóór de afrekenperiode, ook
   als die bij een ander contract of een andere leverancier hoorden. Elke dag
   volgt dezelfde dag een jaar eerder; een dag daarvan zonder metingen komt
   uit het jaarverbruik van die periode. Dit telt alleen als minstens 90% van
   de dagen van die periode gemeten of geïmporteerd is;
3. anders is er **geen prognose**: de kaart zegt dan welk onderdeel ontbreekt
   en hoeveel van de vorige periode gemeten is. Een onderdeel dat nooit
   gemeten is (bijvoorbeeld teruglevering zonder zonnepanelen) is niet nodig.
   Bij *stroom en gas apart* krijgt het andere onderdeel gewoon een
   prognose; bij één contract voor alles is er dan geen prognose.

Gebruik je tot nu toe stelselmatig meer of minder dan in dezelfde dagen van
de vorige periode (minstens 60 dagen om te vergelijken; bij gas minstens 30
in oktober t/m april), dan volgt de rest van de periode die **trend**: 10%
minder tot nu toe is 10% minder voor de rest. Bij een dynamisch contract
gelden de bekende marktprijzen (ook van vandaag en morgen), en daarna het
gemiddelde per uur van dezelfde maand vorig jaar of anders van de laatste 30
dagen.

**De bandbreedte** is de verwachte uitkomst plus en min een marge. Die komt
uit onafhankelijke onzekerheden, gecombineerd als de wortel uit de som van
de kwadraten (ze tellen dus niet gewoon op):

| Onzekerheid | Over | Automatisch |
|---|---|---|
| Verbruik dat de vorige periode volgt | de verwachte verbruikskosten van afname, teruglevering, gas of water | berekend: hoeveel de maanden van deze periode afwijken van dezelfde maanden van de vorige periode (na de trend), tussen 3 en 25%; met minder dan 2 maanden om te vergelijken 8% |
| Ingevuld jaarverbruik | idem | 10% |
| Marktprijzen (dynamisch) | alleen het marktprijsdeel van de kwartieren waarvan de prijs nog niet bekend is, niet de opslag en de energiebelasting | 10% |
| Geschatte dagen in het verleden | hun deel van de kosten tot nu | als het verbruik |

Onder de kaart staan de percentages die de prognose gebruikt, met waar ze
vandaan komen (*berekend uit 4 maanden*, *standaard* of *zelf ingesteld*).
Je past ze aan onder **Instellingen → Prognose (geavanceerd)**: een eigen
waarde gaat voor tot je hem terugzet op *Automatisch*; ook de trend kun je
daar uitzetten en de 90% en 92% wijzigen. Daar zie je ook wat de automatische
waarde nu is.

**Vergelijken ("wat als").** Onderaan de weergave **Kosten** maak je scenario's: een
contract zonder datums, met andere tarieven, bijvoorbeeld een vast tarief of
een aanbieding. Begin desgewenst met je eigen contract als startpunt. Per maand
zie je je werkelijke kosten naast die van elk scenario, met het verschil over
de periode (afgelopen 12 maanden, dit jaar of vorig jaar).

- Een scenario rekent je **gemeten** verbruik door met dezelfde regels, over
  precies de dagen waarop je echte contracten liepen.
- Met *Rekenen met de regels van 2027* zie je wat dezelfde periode zou kosten
  zonder salderen en met teruglevering tegen de kale marktprijs.
- Scenario's tellen nooit mee in je kosten of voorschot.
- Het is een terugblik: toekomstige prijzen zijn anders. Bij een dynamisch
  contract verschuif je vaak verbruik naar goedkope uren, dus een berekende
  besparing is eerder een ondergrens.

**Water.** Heb je een watermeter, dan schakel je bovenaan **Verbruik en Kosten** tussen
*Energie* en *Water*. Water heeft eigen contracten met dezelfde opbouw:
begindatum, optioneel een einddatum, *Nieuwe periode* als vervolg bij een
tariefwijziging (meestal per 1 januari), vaste kosten per maand (vastrecht),
een voorschot en een verwacht jaarverbruik in m³. Per m³ vul je het tarief
en de belasting op leidingwater in, beide incl. btw; de belasting telt over de
eerste 300 m³ per kalenderjaar. Voorschot, prognose en advies werken zoals bij
energie, met een vrijwel vlak jaarprofiel. Rioolheffing en waterschapsheffing
zijn gemeentelijke belastingen; wil je ze meetellen, voeg ze dan toe als vaste
post. Op het **Dashboard** staat naast *Kosten vandaag* een kaart *Waterkosten
vandaag*. Water heeft geen scenario's: je waterbedrijf kies je niet zelf.

**Snelheid.** Afgesloten dagen worden één keer berekend en per dag
bewaard; alleen vandaag wordt steeds opnieuw berekend. Nieuwe of gewijzigde
kwartierdata, prijzen of contracten maken de bewaarde dagen vanaf de eerste
geraakte dag ongeldig (door het salderen hangt een dag af van de dagen ervoor
in hetzelfde jaar). Een heel jaar per maand kost daardoor minder dan een
milliseconde rekenwerk.

## Plugins (extra apparaten naast HomeWizard)

Naast de HomeWizard-energieapparaten kun je andere apparaten uitlezen via
**plugins**: aparte integraties die naast de energiekern draaien, zoals
**Mitsubishi Heavy Industries**-airco's met de WF-RAC-wifimodule (de Smart
M-Air-app), **tado°** en **Tuya / Smart Life**. Een plugin leest alleen, tenzij
je bij die plugin **Bediening toestaan** aanzet (zie hieronder).

Beheer ze onder **Instellingen → Plugins**:

1. Kies een plugin-type en voeg het toe. Je krijgt een configuratiescherm dat
   bij dat type past.
2. Voeg binnen de plugin apparaten toe: handmatig op IP-adres, of laat het
   netwerk **scannen**.
3. Op het tabblad **Klimaat Plugins** (na Apparaten) verschijnt een tegel per apparaat
   met de waarden die eruit komen. Dat tabblad is alleen zichtbaar als er
   minstens één plugin-apparaat is.
4. Meet een tussenstekker of kWh-meter het apparaat (een airco, een
   elektrische radiator)? Koppel hem op de tegel met **Meter koppelen**: één
   meter per apparaat. Zitten meerdere apparaten op één groep (bijvoorbeeld
   airco-units), kies dan bij elk dezelfde meter; de tegel meldt dan dat het
   vermogen en de kosten van die apparaten samen zijn. Bij tado-kamers kan
   dit niet (die gebruiken zelf geen stroom). De tegel toont het vermogen nu
   en het verbruik en de kosten van vandaag, met een link naar Kosten voor
   die meter. Onder
   **Historie** staat een meetwaarde van het apparaat (zoals de
   kamertemperatuur) van de afgelopen 24 uur naast het vermogen van de
   gekoppelde meter. Gas per kamer of radiator kan niet: de P1-meter meet
   alleen het hele huis.

5. **Bedienen** (airco, tado en Tuya): zet onder Instellingen → Plugins bij
   de plugin **Bediening toestaan** aan. De tegel krijgt dan knoppen: aan/uit,
   modus, temperatuur, ventilator, de flap boven/onder en links/rechts en
   3D auto bij de airco (een gekozen flapstand zet 3D auto uit); temperatuur, uit en *terug
   naar schema* bij een tado-kamer; bij een Tuya-apparaat (zoals een
   elektrische radiator) wat het apparaatmodel schrijfbaar noemt, meestal
   aan/uit, ingestelde temperatuur en modus. Het kinderslot wordt alleen
   getoond. Tuya-bediening werkt alleen voor apparaten die via scannen zijn
   toegevoegd (het model komt uit je Tuya IoT-project); scan een apparaat dat
   je al had opnieuw om het bij te werken. Bij een tado-kamer kies je de
   **Stand**: *Schema* (tado volgt zijn eigen schema), *Handmatig* (houdt de
   huidige temperatuur vast) of *Uit*; een handmatige stand loopt na
   `overlay_minutes` vanzelf af als de gateway hem niet meer verlengt. Met
   **Bedienen** bij een apparaat zet je de bediening voor alleen dat apparaat
   uit; de schakelaar van de plugin gaat voor. Standaard staat bediening uit:
   zonder wachtwoord (zie Beveiliging) kan iedereen op je netwerk die de app
   opent dan bedienen. Elke opdracht komt in het log van de gateway.
6. **Eigen tabbladen**: onder Instellingen → Plugins maak je hooguit drie
   tabbladen met een eigen naam (bijvoorbeeld *Verwarmen*) en kies je per
   apparaat op welk tabblad het staat. Apparaten zonder eigen tabblad blijven
   op *Klimaat Plugins*; een tabblad zonder apparaten wordt niet getoond.
   Op een telefoon lopen de tabbladen bovenin door over meerdere regels.
   Waar een tabblad in de rij staat kies je onder **Instellingen →
   Tabbladen** (zie [Volgorde van de tabbladen](#volgorde-van-de-tabbladen)).
7. **Eigen dashboardtegels**: met **Dashboardtegel maken** bovenaan *Klimaat Plugins*,
   of **Op dashboard** bij een apparaat, maak je tegels voor het
   **Dashboard** onder *Klimaat Apparaten*. Een tegel heeft een eigen naam, is smal
   of breed, heeft een **vorm** en bevat zoveel onderdelen als je wilt (tot 12).
   De vormen: *Alleen de waarde* (elke waarde groot onder de vorige), *Lijst*
   (naam links, waarde rechts), *Raster* (twee kolommen), *Eén grote waarde*
   en *Waarde met lijntje* (met het verloop van de laatste 6 uur). Onderdelen:
   - een **waarde**, bijvoorbeeld de buitentemperatuur van de airco of de
     kamertemperatuur van tado. Standaard alleen de waarde onder de naam die
     je kiest; per waarde kun je aanzetten: de naam van het apparaat, het
     verloop van het laatste uur, de laagste en hoogste van vandaag, en
     *Ingesteld op* (bijvoorbeeld "18,2 °C → 20,0 °C");
   - een **status**: de stand van een apparaat als label, bijvoorbeeld
     "Verwarmen · 22 °C" of "Aan";
   - een **grafiek** van 1 tot 4 meetwaarden, ook van verschillende apparaten
     (binnen naast buiten), over 6 uur, 24 uur of 7 dagen;
   - een **bediening** van het apparaat (alleen als bediening aan staat).

   Zolang je geen tegel hebt gemaakt, toont het dashboard er niets van. Met
   **Aanpassen** op het dashboard zet je tegels in een andere volgorde,
   bewerk of verwijder je ze en voeg je er meer toe (tot 20). De tegels staan
   in de database van de gateway, dus je ziet dezelfde op elk apparaat.

Getallen uit plugin-metingen worden bewaard voor die historie, even lang als
de minuutdata (`HWLOCAL_RETENTION_DAYS`).

De metingen lopen realtime over dezelfde verbinding als de energieapparaten
(`/api/stream`). Plugins en apparaten worden in de database bewaard; er zijn
geen omgevingsvariabelen voor nodig.


### Volgorde van de tabbladen

Onder **Instellingen → Tabbladen** zet je de tabbladen in de volgorde die je
wilt: open *Volgorde aanpassen* en gebruik ↑ en ↓ per tabblad; bijvoorbeeld een eigen tabblad *Verwarmen*
direct naast *Dashboard*. Een eigen tabblad dat nog geen apparaten heeft
staat er ook in (met *nu verborgen*). De volgorde wordt in de gateway bewaard
(`tab-order.json` in de datamap) en geldt dus op elke telefoon en pc; een
nieuw eigen tabblad komt eerst na *Klimaat Plugins*. *Standaardvolgorde*
zet alles terug.
### Mitsubishi Heavy (WF-RAC)

Leest lokaal en zonder authenticatie uit (HTTPS op poort 51443): aan/uit, modus
(koelen/verwarmen/auto/ventileren/ontvochtigen), ingestelde temperatuur,
ventilatorstand, kamer- en buitentemperatuur, of de compressor draait, en een
eventuele foutcode. De airco moet in hetzelfde netwerk zitten als de gateway.
"Netwerk scannen" zoekt WF-RAC-modules in je lokale netwerk; vul eventueel een
afwijkend bereik in (bijv. `192.168.1.0/24`).

Bedienen: bij de eerste opdracht koppelt de gateway zich als account aan de
wifimodule (`hwlocal`). Een module houdt **maximaal 4** gekoppelde apps bij;
de Smart M-Air-app is er meestal één. Is de lijst vol, dan meldt de tegel dat;
verwijder dan een oude koppeling in de Smart M-Air-app. Een airco heeft geen
eigen timer: stel bij de plugin eventueel **Automatisch uitzetten na (uur)**
in, dan zet de gateway een airco die hij zelf aanzette na die tijd weer uit
(alleen zolang de gateway draait).

### tado° (verwarming)

tado° heeft geen lokale API, dus deze plugin werkt via de tado-cloud — het enige
plugin-onderdeel dat niet LAN-only is. Je logt **eenmalig** in: klik "Verbinden
met tado", open de getoonde link, log in bij tado en keur goed. De gateway
bewaart daarna alleen een vernieuwbaar token (nooit je wachtwoord). Met "Kamers
ophalen" verschijnen je **kamers** als tegels (de bridge niet). Per kamer zie je
temperatuur, ingestelde temperatuur, luchtvochtigheid, verwarmingsvermogen,
aan/uit en open raam. Om binnen tado's daglimiet te blijven leest de gateway
alle kamers in één keer, elke 15 minuten.

Bedienen: een temperatuur of *uit* vanuit deze app is altijd een tijdelijke
tado-instelling met een timer (standaard 120 minuten, in te stellen bij de
plugin). Zolang de gateway draait, verlengt hij die vóór het einde; valt de
gateway uit, dan gaat tado vanzelf terug naar het eigen schema. Verander je de
kamer in de tado-app, dan laat de gateway hem los. Elke instelling en
verlenging telt mee voor tado's daglimiet (een kamer die een dag op een
handmatige stand blijft, kost bij 120 minuten zo'n 12 extra verzoeken).

Ontwerp en protocol van alle plugins staan in `docs/PLUGINS.md`.

### Thuisbatterij (via P1-meter)

De Plug-In Batteries van HomeWizard worden niet op de batterij zelf
aangestuurd, maar via de P1-meter (of een kWh-meter): die houdt bij wat de
batterijen samen doen (`/api/batteries`, API v2). Deze plugin gebruikt de
meter die de gateway al uitleest, dus er valt niets in te vullen. Wel moet
de P1-meter **via API v2 gekoppeld** zijn (Apparaten → Koppelen; een
P1-meter op API v1 heeft geen batterijbediening).

Voeg bij Instellingen → Plugins *Thuisbatterij (via P1-meter)* toe en klik
*Netwerk scannen*: de meter met batterijen verschijnt. De tegel toont de
stand, het vermogen (+ laden, − ontladen), het doelvermogen, de gemiddelde
laadtoestand van de batterijen die de gateway uitleest, of laden en
ontladen zijn toegestaan, de maximale vermogens en het aantal batterijen.

Met *Bediening toestaan* kies je de **stand**:

| Stand | Wat de meter doet | API |
|---|---|---|
| Nul op de meter | laden bij overschot, ontladen bij verbruik | `mode: zero`, beide permissies |
| Alleen laden | wel laden, niet ontladen (bewaren voor later) | `zero` + `charge_allowed` (API 2.2) |
| Alleen ontladen | niet laden, wel ontladen | `zero` + `discharge_allowed` (API 2.2) |
| Stand-by | niets | `permissions: []` (oudere firmware: `mode: standby`) |
| Volladen | laden tot 100%, ook van het net; daarna terug naar de vorige stand | `charge_to_full` (oudere firmware: `mode: to_full`) |
| Slim (HomeWizard) | het plan van HomeWizard op basis van verwachting en prijzen | `mode: predictive` (API 2.3); werkt alleen als de P1-meter met de HomeWizard-cloud verbonden is |

Na de eerste uitlezing biedt de app alleen de standen aan die de firmware
van de meter kent. Bediening loopt zoals bij elk apparaat via dezelfde weg
(controleren, tot drie pogingen, de laatste opdracht wint), ook vanuit een
automatisering met *Apparaat bedienen* → Thuisbatterij → Stand. Het
voorbeeld *Thuisbatterij laden bij goedkope stroom* laadt in de vier
goedkoopste uren van de dag en zet daarna terug op nul op de meter. Kies je
*Volladen* bij een volle batterij, dan gaat de meter meteen terug naar de
vorige stand; de opdracht meldt dan dat het apparaat de stand niet
overneemt.

### ista (verwarming, mijn.ista.nl)

Heb je ista-meters op je radiatoren (warmtekostenverdelers) en een account op
[mijn.ista.nl](https://mijn.ista.nl), dan leest de plugin **ista
(verwarming)** je verwarmingsverbruik per radiator. ista heeft geen
officiële API: de gateway logt in zoals je browser doet en gebruikt de
gegevens die de website zelf ophaalt. Daarom bewaart hij je gebruikersnaam en
wachtwoord, **versleuteld** in de database (zie Beveiliging).

1. **Instellingen → Plugins → Plugin toevoegen → ista (verwarming)**: vul je
   gebruikersnaam en wachtwoord van mijn.ista.nl in.
2. **Radiatoren ophalen** (scannen) en voeg ze toe: een tegel per radiator en
   één voor het totaal op het tabblad Klimaat.
3. De eerste keer haalt de gateway **elke dag sinds je intrek** op (de stand
   per radiator aan het eind van de dag en het gebouwgemiddelde); dat duurt
   rustig aan zo'n half uur: eerst de maandcijfers, dan de dagen, nieuwste
   eerst, zodat je snel de lopende periode ziet. De voortgang en de
   geschatte resttijd staan op de Verwarming-pagina en bij de plugin; na een
   herstart gaat het verder waar het was. Daarna elke ochtend de laatste 14 dagen opnieuw
   (ista werkt recente standen bij), eens per week het gebouwgemiddelde van
   de lopende periode (ook dat stelt ista achteraf bij), en de maandcijfers.
   Weigert ista het wachtwoord, dan stopt de gateway tot je het aanpast, zodat
   je account niet geblokkeerd raakt.

Bij **Verbruik en Kosten → Verwarming**, met dezelfde keuzes als bij energie
en water: de kamer (of alle kamers) op de plek van het apparaat, de periode
(gisteren, 7 dagen, maand, jaar, *Contractperiode* = een afrekenperiode van
ista van 1 juli t/m 30 juni, of alles; vandaag niet, ista heeft alleen
dagstanden die achterlopen) en de weergave:

- **Verbruik**: in *eenheden* (waarop je afrekent: tikken × factor van de
  radiator, min de korting voor de ligging) of in *tikken* (wat de meter
  telde), met dezelfde dagen vorig jaar en het gebouwgemiddelde. Onder *Toon
  als tabel* elke kamer per dag, week, maand of periode. Bij *Gisteren* het
  verbruik per kamer van die ene dag (‹ › per dag); heeft ista die dag nog
  niet gemeten, dan staat er welke dag de laatst gemeten is.
- **Alleen gemeten dagen**: heeft ista de radiatoren een paar dagen niet
  uitgelezen, dan geeft mijn.ista.nl voor die dagen eigen schattingen
  (standen met decimalen, daarna standen die niet meer oplopen) en een
  gebouwgemiddelde van 0. Die schattingen (en vandaag, dat nog loopt) gebruikt
  de app nergens: verbruik, gebouwgemiddelde, vorig jaar, kosten, voorschot
  en prognose, de dashboardkaart, de plugin en het rapport stoppen bij de
  laatste dag die ista gemeten heeft; de prognose rekent de rest van de
  periode vanaf die dag. Bovenaan staat *Gemeten t/m …*, en loopt ista meer
  dan een dag achter dan zegt de app dat de dagen daarna nog niet uitgelezen
  zijn. Er hoeft niets te gebeuren: de gateway haalt elke ochtend de laatste
  14 dagen opnieuw op en vult ze aan zodra ista de echte standen heeft. De
  plugin heeft daarvoor de meetwaarde *Gemeten t/m* (`last_measured`).
- **Kosten**: net als bij water de kosten van de gekozen dagen (verbruik ×
  prijs per eenheid plus het vaste deel naar rato), de voorschotkaart van de
  afrekenperiode en de lijst **Verwarmingscontracten**: per afrekenperiode een
  contract met je vloeroppervlak (in dm², 71 m² = 7.100 dm²), je voorschot per
  maand en, zodra die er is, de getallen van de **jaarafrekening** (variabele
  en vaste energiekosten, overige kosten, diensten ista, eenheden en
  vloeroppervlak van het complex). Tijdens de periode rekent de app met de
  prijzen van de vorige afrekening (eventueel met een correctie in procent, of
  eigen prijzen). De rest van de periode schat hij met het *verwacht
  jaarverbruik* uit het contract als je dat invult (verdeeld met het
  stookpatroon van het gebouw of een standaard stookprofiel), anders met
  dezelfde dagen van de vorige ista-periode, als ista daarvan minstens 90% van
  de dagen gemeten heeft (een dag zonder meting komt uit het stookprofiel);
  anders is er geen prognose en zegt de kaart waarom. Je ziet verwachte
  kosten met bandbreedte (apart de vaste kosten en de verbruikskosten van de
  hele periode), verwacht bij- of terugbetalen en een advies voor het
  voorschot. De marge combineert de onzekerheid van het verbruik (berekend uit
  de maanden tot nu tegenover de vorige periode, tussen 3 en 25%, anders 15%;
  met een ingevuld jaarverbruik 20%) en, zolang de prijs uit de vorige
  afrekening komt, ±10% op de prijs; aan te passen onder *Instellingen →
  Prognose (geavanceerd)*. Met *Afrekening
  ontvangen* wordt de periode definitief en controleert de app of zijn
  berekening op de cent gelijk is aan de afrekening. Daar vul je de
  totalen van het hele complex in zoals ze onder "Berekening
  eenheidsprijzen" staan (kosten in euro's, eenheden en vloeroppervlak van
  alle woningen samen), niet de prijzen: die rekent de app eruit en toont
  ze meteen in het formulier. Heb je alleen de prijzen, vul ze dan in bij
  *Prijzen* en laat de afrekening uit. Met een voorschot toont het contract
  daarna de uitkomst ter controle: afrekening min betaald voorschot, met
  het bedrag dat je bijbetaalt of terugkrijgt.

Op het dashboard staat een kaart *Verwarming dit seizoen*. Simulator: hwsim
heeft een nep-mijn.ista.nl op :18081 (`HWLOCAL_ISTA_URL`, account
`sim@example.org` / `sim`; met `hwsim -ista-stale 5` heeft ista de laatste
5 dagen niet gemeten); `scripts/dev-sim.sh` zet dat al. Ontwerp:
[docs/ISTA.md](ISTA.md).

## Meldingen en automatisering

Er zijn twee soorten regels, elk op een eigen tabblad:

- **Meldingen** sturen alleen een pushmelding, ook als de app dicht is. Ze
  kunnen op elke waarde: verbruik vandaag, een temperatuur binnen of buiten,
  de stroom- of gasprijs, een apparaat dat offline gaat.
- **Automatisering** schakelt Energy Sockets en bedient plugin-apparaten
  (airco, tado, Tuya), met desgewenst ook een pushmelding. Een
  automatisering kan iets anders doen zodra haar voorwaarden **niet meer**
  gelden (bijvoorbeeld de airco weer uit en de CV terug op schema).

*Kopiëren* op de kaart van een regel opent het formulier met een kopie
(naam + *Kopie*); de kopie staat na opslaan uit, zodat ze niet naast het
origineel hetzelfde apparaat bestuurt.

Een regel heeft:

- **voorwaarden** die allemaal moeten gelden (*alle voorwaarden*) of waarvan
  er één genoeg is (*één van de voorwaarden*). Een voorwaarde vergelijkt een
  waarde met *lager dan*, *hooguit*, *hoger dan*, *minstens*, *gelijk aan* of
  *niet gelijk aan*:
  - van een HomeWizard-apparaat: vermogen, waterverbruik, laadniveau, online,
    aan/uit, meterstanden, en het verbruik van **vandaag** (afname,
    teruglevering, netto afname, gas, water);
  - van een plugin-apparaat: bijvoorbeeld de kamertemperatuur van tado, de
    kamer- en buitentemperatuur van de airco, of de modus (tekst, bijv. *is
    Verwarmen*);
  - **prijzen**: stroom en gas *kaal* (marktprijs zonder btw) of *all-in*
    (volgens het energiecontract van die dag: opslag, energiebelasting en
    btw, zonder vaste kosten en saldering), de gemiddelde stroomprijs van
    vandaag, en *uren vandaag goedkoper* (lager dan 4 = het is nu een van de
    4 goedkoopste uren van de dag);
  - een **tijdvenster**: weekdagen plus van/tot (in `HWLOCAL_TZ`; *tot* telt
    niet mee, over middernacht kan, van = tot is de hele dag), *binnen* of
    *buiten* dat venster.

  Vergelijken kan met een getal of met **een andere waarde**, elk met een
  factor: *0,5 × gasprijs all-in hoger dan 0,8 × stroomprijs all-in* (plus
  eventueel een vast getal). Zo vergelijk je wat een half uur stoken kost met
  de CV (0,5 m³ gas) of met de airco (0,8 kWh), of *binnen lager dan buiten
  + 2*;
- een **duur**: zo lang moeten de voorwaarden onafgebroken gelden. Een
  meting die er niet is (apparaat offline, geen prijs) onderbreekt de tijd;
- bij een automatisering de **acties**: een socket aan- of uitzetten, een
  plugin-apparaat bedienen (*+ Apparaat bedienen*, alleen als bij die plugin
  *Bediening toestaan* aan staat) en/of een pushmelding, plus optioneel de
  acties voor *als de voorwaarden niet meer gelden*. De pushmelding van een
  automatisering is kort: als titel de naam van de automatisering, als tekst
  alleen de reden: *Afgegaan.* (of je eigen tekst), *Verder na het
  wachten.*, *Bijgeregeld van 21 °C naar 21,5 °C.* of *Gestopt.* (met de
  reden, zoals *Gestopt: Airco staat uit.*). Het schakelen en bedienen
  wordt gecontroleerd en zo nodig herhaald, net als vanuit de app (zie
  [Bedienen](#bedienen)); op de kaart staat dan bijvoorbeeld *Airco: Aan
  aan (poging 2).* Mislukt het schakelen of bedienen, dan
  krijg je daar altijd een pushmelding van, ook als de automatisering zelf
  geen pushmelding als actie heeft; wat er precies gebeurde staat op de
  kaart van de automatisering. Een melding (tabblad Meldingen) noemt wel wat
  ze zag. Daarnaast (alleen bij de acties zelf):
  - een **wachtstap** (*+ Wachtstap*): wacht tot een voorwaarde geldt, hooguit
    een aantal minuten, of een vaste tijd; daarna volgen de acties die bij de
    wachtstap horen. De wachtstap loopt door als de voorwaarden van de regel
    intussen niet meer gelden, behalve als de automatisering acties heeft
    voor als de voorwaarden niet meer gelden: die gaan dan af en de
    wachtstap stopt, zodat de acties erna ze niet ongedaan maken;
  - **regelen** (*+ Regelen*, altijd de laatste actie): houdt een meetwaarde
    (bijvoorbeeld de kamertemperatuur van tado of een thermometer) op een
    doel ± marge door de temperatuur van een airco of elektrische radiator
    elke zoveel minuten één stap omhoog of omlaag te zetten. Binnen de marge
    blijft de stand staan; na een wijziging met de afstandsbediening gaat het
    verder vanaf die stand. Het regelen stopt als het apparaat uit gaat en,
    als je dat aanvinkt, aan het eind van het tijdvenster van de regel
    (desgewenst gaat het apparaat dan uit). Een thermostaat zoals tado regelt
    de CV zelf en is hier niet te kiezen; als meetwaarde kan tado wel.
    - **Afwijking** (*apparaat op doel +*): een airco of radiator meet bij
      het apparaat, dus om de kamer op 19 °C te houden moet hij vaak op 20 °C
      staan. Dat verschil (hier +1) is de afwijking; een nieuwe regelstap
      begint met +1. Zonder *eerst snel naar het doel* gaat het apparaat bij
      de start op doel + afwijking en regelt het vandaar in stappen.
    - **Bereik** (*Meer instellingen*, *tussen doel − … en doel + …*): de
      stand blijft daarbinnen, standaard 2 °C onder en 2 °C boven het doel
      (bij doel 19: 17 tot 21 °C). Het bereik ligt rond het doel, de
      afwijking verschuift het niet. Het schuift wel mee als je het doel op
      de kaart aanpast. Automatiseringen van voor deze instelling houden hun
      vaste minimum en maximum (een minimum boven het doel staat dan als een
      negatief getal bij *doel −*).
    - **Eerst snel naar het doel**: is het bij de start kouder dan doel −
      marge, dan gaat het apparaat meteen op de bovenkant van het bereik;
      warmer dan doel + marge, dan op de onderkant. Zodra de meetwaarde binnen
      doel ± marge komt, gaat de stand naar doel + afwijking, en daarna wordt
      er gewoon in stappen geregeld. Begint de meetwaarde al binnen de marge,
      dan wordt er meteen gewoon geregeld. Op de kaart staat tijdens het
      opwarmen *snel opwarmen tot doel − marge*.

  Doel en marge van het regelen pas je op de kaart aan met − en + (per 0,1);
  dat herstart het regelen niet. Onder *Grafiek van de laatste 24 uur* op de
  kaart zie je de meetwaarde, de stand van het apparaat en het doel ± marge
  (uit de historie van de plugin-apparaten). De gateway houdt per
  automatisering bij wanneer ze regelde; die tijden zijn gekleurd en de
  stand van het apparaat staat alleen in die tijden in de grafiek, want een
  andere regel kan hetzelfde apparaat op andere momenten bedienen. Heeft de
  automatisering de laatste 24 uur niet geregeld, dan staat er geen grafiek.
  Ter controle kun je in het
  formulier *Pushmelding bij elke stap en als het regelen stopt* aanzetten:
  elke stap (*Start: … op …*, *Bijgeregeld van … naar …*, *Snel opwarmen: …*, *Doel bijna
  bereikt: … terug naar …*) en het stoppen (met de reden) komen dan als
  pushbericht. Elke stap staat ook altijd in het log van de gateway
  (`regel: regelen` met meetwaarde, oude en nieuwe stand). De overige instellingen staan in het
  formulier onder *Meer instellingen*. Op een telefoon staan de velden van
  het formulier onder elkaar; instellen gaat het makkelijkst op een groter
  scherm.

  Waar een automatisering is (wachtstap of regelen) wordt bewaard, zodat ze
  na een herstart verder gaat. Pas je een automatisering aan die al is
  afgegaan, dan gaat ze niet opnieuw af zolang de voorwaarden gelden: de
  acties vóór de wachtstap of het regelen worden niet nog eens gestuurd. Een
  wachtstap loopt door (met de nieuwe wachttijd, gerekend vanaf het begin van
  het wachten) en daarna komen de acties zoals je ze nu hebt ingesteld.
  Regelen gaat door met de nieuwe waarden; dat geldt ook als je de naam, de
  voorwaarden of andere acties verandert. Valt de ingestelde temperatuur
  buiten het nieuwe bereik, dan zet hij die bij de volgende controle
  erbinnen. Zet je *eerst snel naar het doel* uit terwijl hij snel opwarmt
  of afkoelt, dan gaat hij bij de volgende controle naar doel + afwijking en
  regelt daarna in stappen; aanzetten werkt pas bij een volgende start.
  Opnieuw beginnen doet hij alleen als je een ander apparaat, een andere
  stand of een andere meetwaarde kiest om op te regelen (of op die plek een
  ander soort stap zet), en na uit- en weer aanzetten van de regel; hij gaat
  dan meteen af als de voorwaarden gelden. Uitschakelen of verwijderen stopt
  wat hij deed.

Een regel gaat **één keer** af en pas weer nadat zijn voorwaarden een keer
niet golden. Een nieuwe of gewijzigde **melding** wacht daar ook op: zo meldt
"droger < 5 W gedurende 3 minuten" alleen na een droogbeurt en niet als de
droger al uit stond. Een nieuwe of gewijzigde **automatisering** kijkt meteen
naar de situatie van dat moment; een automatisering die al is afgegaan en
die je aanpast, gaat niet opnieuw af zolang de voorwaarden gelden. De
toestand wordt bewaard, dus een herstart herhaalt niets en vergeet niets.

### Handmatige acties

Een automatisering kan ook **handmatig** starten (*Starten: handmatig,
vanaf het dashboard*). Zo'n actie heeft geen voorwaarden en geen duur: je
zet hem zelf aan en uit, op zijn kaart onder Automatisering of op een
dashboardtegel (*Bediening* → kies de actie onder *Acties*). Daarmee bedien
je meerdere apparaten tegelijk of laat je een airco of radiator regelen
zonder dat er voorwaarden voor nodig zijn, bijvoorbeeld *Woonkamer warm*:
airco aan en regelen tot de kamer 19 °C is, bij uitzetten airco uit.

- **Aan** werkt als het afgaan van een automatisering: de acties, de
  wachtstap en het regelen gaan precies zo (doel en marge pas je aan met −
  en + op de kaart, op de tegel alleen het doel). **Uit** voert de acties *bij uitzetten*
  uit, stopt een wachtstap en laat het regelen stoppen zoals bij een
  automatisering waarvan de voorwaarden niet meer gelden.
- Heeft de actie geen acties bij uitzetten, geen wachtstap en geen regelen
  (bijvoorbeeld *Alles uit*: twee sockets uit), dan is het een **knop**
  *Uitvoeren*: de acties gaan één keer, daarna is de knop weer klaar.
- Een actie gaat vanzelf uit, zonder de acties bij uitzetten, als er niets
  meer loopt: de laatste wachtstap is voorbij, of het regelen stopte omdat
  het apparaat uit ging (met de eigen afstandsbediening of app). Zo staat
  er geen *Aan* terwijl er niets meer gebeurt.
- Of een actie aan staat wordt bewaard: na een herstart regelt hij verder.
  Een knop die net was ingedrukt toen de gateway stopte, gaat niet opnieuw.
  Uitschakelen van de actie (of omzetten naar *als de voorwaarden gelden*)
  zet hem uit. Een uitgeschakelde actie kan niet starten; de tegel zegt dat
  en verwijst naar Automatisering. Een automatisering of kopie die je op
  *handmatig* zet, wordt ingeschakeld opgeslagen (een gewone kopie staat na
  opslaan uit), want een actie start alleen als jij hem aanzet.
- Een actie en een automatisering (of bediening vanuit de app) blokkeren
  elkaar niet: wat het laatst een opdracht stuurt, geldt.

Voorbeelden staan klaar. Bij Meldingen: *Droger of wasmachine klaar*, *Veel
stroom vandaag*, *Veel gas vandaag*, *Water loopt lang*, *Binnen te koud*,
*Vorst buiten*, *Negatieve stroomprijs*, *Apparaat offline* en (met tado en
airco of een Tuya-radiator) *CV en airco tegelijk*. Bij Automatisering:
*Verwarmen: eerst CV, dan airco*, *Airco: snel opwarmen, dan op
temperatuur houden*, *Goedkoopst verwarmen: airco of CV*,
*Socket aan bij goedkope stroom* (de 4
goedkoopste uren), *Teruglevering opmaken* en *Airco uit als het raam open
staat*. Er verschijnen alleen voorbeelden waarvoor je de apparaten hebt.

Voorbeeld met hysterese, de woonkamer met CV (tado) en airco:

1. *Woonkamer warm*: tado-temperatuur hoger dan 20,9 °C gedurende 10 min →
   tado *Uit*, airco *Modus Verwarmen*, airco *Temperatuur 21 °C*, airco
   *Aan*.
2. *Woonkamer koud*: tado-temperatuur lager dan 19,6 °C gedurende 10 min →
   tado *Terug naar schema*, airco *Aan uit*.

Hetzelfde met een wachtstap en regelen, in één regel (het voorbeeld
*Verwarmen: eerst CV, dan airco*):

- als: tijdvenster 06:30-22:00, tado-temperatuur lager dan 18,5 °C en airco
  *Aan* is nee;
- dan: tado *Temperatuur 19 °C* → *wachten tot* tado-temperatuur minstens 19
  °C, hooguit 30 minuten → tado *Terug naar schema*, airco *Verwarmen*, *Aan*,
  *21 °C* → *regelen*: airco-temperatuur zodat tado 19 ± 0,2 °C blijft,
  afwijking +2, elk kwartier 0,5 °C, tussen 18 en 23 °C (doel − 1 en doel
  + 4), stoppen aan het eind van het tijdvenster en dan uit.

De airco meet aan het plafond; daarom staat hij hoger dan het doel en regelt
hij op de meting van tado.

Zonder CV, met de airco alleen (het voorbeeld *Airco: snel opwarmen, dan op
temperatuur houden*): als het tussen 7:00 en 22:00 onder 18,5 °C is → airco
*Verwarmen*, *Aan* → *regelen* op 19 ± 0,2 °C, afwijking +1, bereik 2 onder
en 2 boven het doel, *eerst snel naar het doel*. De airco gaat dan op 21 °C
tot de kamer 18,8 °C is, daarna op 20 °C, en regelt dan in stappen tussen 17
en 21 °C.

Gebruik als voorwaarde liever niet *airco staat uit* samen met acties bij
*als de voorwaarden niet meer gelden*: de automatisering zet de airco zelf
aan, waardoor die voorwaarde meteen niet meer geldt en die acties direct
volgen (zet die de airco uit, dan stopt ook het regelen). Dat de regel maar
één keer afgaat, regelt hij zelf al; voor uitzetten aan het eind is *stoppen
als het tijdvenster eindigt* met *dan uitzetten* bedoeld.

Omdat tado's instelling een tijdelijke is die de gateway verlengt, valt de
CV bij uitval van de gateway vanzelf terug op het tado-schema. De airco heeft
zo'n terugval niet; zet daarvoor bij de MHI-plugin *Automatisch uitzetten na
(uur)*.

Een tijdvenster gaat af zodra het begint (een regel gaat immers af als zijn
voorwaarden gaan gelden), bijvoorbeeld *badkamer ma-vr 06:30-07:30 → tado
badkamer 22 °C*, met als *niet meer gelden*-actie *terug naar schema*. Een
puur tijdschema kan tado zelf beter, want dat werkt ook als de gateway uit
staat; gebruik automatisering vooral voor logica over meerdere apparaten
heen en op basis van prijzen.

**Meldingen aanzetten** doe je per telefoon of computer met *Meldingen
aanzetten* bovenaan het tabblad Meldingen; daarna komt er meteen een
testmelding. Pushmeldingen van automatiseringen gaan naar dezelfde apps.
Voorwaarden:

- de app moet via **https** geopend zijn (behalve op de gateway zelf via
  `localhost`). Het makkelijkst is Tailscale, zie
  [HTTPS via Tailscale](#https-via-tailscale);
- op iPhone/iPad alleen in de app op het beginscherm, vanaf iOS 16.4;
- een melding gaat via de pushdienst van de browser (Google, Mozilla of
  Apple). De gateway moet die op internet kunnen bereiken; de inhoud is
  onderweg versleuteld. De sleutel waarmee de gateway berichten
  ondertekent staat in `vapid-key.pem` in de datamap; gooi die niet weg,
  anders moeten alle apps meldingen opnieuw aanzetten.

Schakelen kan alleen bij Energy Sockets via API v1 (net als de schakelaar op
het dashboard). Regels van vóór deze indeling die een apparaat schakelen of bedienen
staan onder Automatisering, de rest onder Meldingen.

## Rapporten en e-mail

Een rapport is een PDF met grafieken en tabellen over een periode. Je maakt
er een onder **Instellingen › Rapporten** (vorige maand, deze maand, vorige
week, contractjaar tot nu, dit jaar, vorig jaar of een eigen periode), of met de knop **PDF** op
*Verbruik en Kosten* voor de periode die je daar ziet. Een rapport kan ook
elke maand of elke week **per e-mail** komen.

Een rapport bestaat uit onderdelen die je aan- of uitzet. Een onderdeel
zonder metingen (geen gas, geen watermeter, geen contract) valt vanzelf weg.
Alle cijfers komen uit dezelfde berekeningen als de app, dus het rapport en
de app zijn het altijd eens.

| Onderdeel | Inhoud |
|---|---|
| Samenvatting | stroom afgenomen en teruggeleverd, gas, water, verwarming en hun kosten, met het verschil met de vorige periode en dezelfde periode vorig jaar; de totale kosten en een tabel *kosten per onderdeel* (stroom en gas, water, verwarming) met het contract, het variabele en het vaste deel; hoeveel van de periode volledig gemeten is en welke stukken ontbreken, geschat of geïmporteerd zijn |
| Stroom | afname en teruglevering per dag (of per maand bij meer dan 62 dagen), het gemiddelde dagprofiel voor werkdagen en weekend, het sluipverbruik (mediaan van het laagste nachtkwartier) met wat dat per jaar kost, en de vijf hoogste kwartieren |
| Gas | verbruik per dag of maand, vergeleken met vorig jaar |
| Kosten | kosten per dag of maand en hun opbouw (levering, energiebelasting, teruglevering, saldering), met de subtotalen *variabele kosten* (wat van je verbruik afhangt) en *vaste kosten*; bij een dynamisch contract de marktprijs die je gemiddeld betaalde (gewogen naar je verbruik) tegenover het gemiddelde, en de goedkoopste en duurste dag |
| Water | verbruik, kosten per dag of maand en hun opbouw (levering, belasting, variabel en vast) met het contract, nachten waarin elk uur water liep (een mogelijk lek) en het verbruik van dit jaar tegenover de 300 m³-grens van de belasting |
| Verwarming | ista, opgebouwd als stroom en water: verbruik per dag of maand, het gebouwgemiddelde, kosten per dag of maand en hun opbouw (eenheden × prijs per eenheid, vloeroppervlak × prijs per dm²), de prijzen per afrekenperiode en waar ze vandaan komen, en tot welke dag ista echt gemeten heeft; dagen die ista nog niet gemeten heeft tellen niet mee. Zonder het onderdeel *Per apparaat* staat hier ook het verbruik per kamer |
| Per apparaat | *Stroom*: verbruik, aandeel en kosten van de stopcontacten en kWh-meters, en wat niet apart gemeten is. *Verwarming (ista)*: per radiator de eenheden, het aandeel en de kosten (eenheden × prijs per eenheid); de vaste kosten (vloeroppervlak) horen bij de woning en staan apart, net zoals de vaste kosten van stroom en gas niet over de apparaten worden verdeeld |
| Wat-als | de opgeslagen scenario's over dezelfde periode |
| Netkwaliteit | de laagste en hoogste spanning per fase en de dips, pieken en stroomuitval die de meter telde |
| Voorschot en prognose | als laatste: de stand van nu, elk in dezelfde vorm: betaald, kosten tot nu, verwachte kosten (met het variabele en het vaste deel), verwachte eindafrekening met bandbreedte en advies. Alleen van wat je verder kiest: stroom en gas bij *Stroom*, *Gas*, *Kosten*, *Samenvatting* of *Wat-als* (alleen *Gas*: alleen het gascontract, of het contract voor stroom en gas samen), water bij *Water*, verwarming (ista) bij *Verwarming*, stroom en verwarming bij *Per apparaat*. Kies je naast dit onderdeel niets dat daarover gaat (alleen *Netkwaliteit*, of niets), dan staat alles erin |

**Automatisch mailen**: een *maandrapport* gaat op de 2e van de maand om
08:00 over de vorige maand, een *weekrapport* op maandag om 08:00 over de
vorige week. Dan zijn de metingen en prijzen van de laatste dag compleet. Bij
*Wat het rapport laat zien* kies je *over die maand of week*,
*contractjaar tot nu* (van het begin van je contractjaar, zoals bij het
voorschot, tot het einde van die maand of week) of *kalenderjaar tot nu*. Met
het onderdeel *Verwarming* kijkt een gepland rapport of ista de hele
periode echt gemeten heeft (ista loopt vaak een paar dagen achter). Is dat
nog niet zo, dan gaat het rapport op tijd zonder verwarming, met in het
rapport en de mail dat de verwarming volgt; ook de kerncijfers en *Per
apparaat* laten de verwarming dan weg, de prognose van verwarming (de stand
van nu) blijft erin. Het overzicht verwarming over dezelfde periode volgt in
een aparte mail ("… – verwarming") zodra ista alles gemeten heeft, en
uiterlijk 7 dagen na het normale tijdstip, dan met tot welke dag ista
gemeten heeft. Een rapport met alleen *Verwarming* (eventueel met *Voorschot
en prognose*) wacht in plaats daarvan, op dezelfde manier. Wat nog volgt
blijft bewaard als de gateway herstart. *Nu versturen* stuurt de hele
periode, met verwarming, en dan volgt er niets meer; haal je *Verwarming*
uit het rapport of wissel je tussen maand en week, dan vervalt wat nog
volgde. Een nieuw gepland rapport begint bij de volgende periode; *Nu versturen* stuurt
de laatste meteen. Elk gepland rapport heeft eigen ontvangers (hoogstens 10)
en onderdelen, en de mail bevat ook de kerncijfers in de tekst. Lukt
versturen niet, dan staat de reden bij het rapport en probeert de gateway
het na een uur opnieuw. Stond de gateway lang uit, dan wordt alleen de
laatste periode nagestuurd, geen oudere.

**E-mail instellen** (Instellingen › E-mail): de gateway verstuurt via een
mailserver die je opgeeft, nooit rechtstreeks naar de ontvanger (vanaf een
thuisaansluiting is poort 25 vaak dicht, en wat er wel doorkomt is spam).

- de mailserver van je provider of je eigen domein, of een dienst als Brevo
  of SMTP2GO;
- **Gmail**: `smtp.gmail.com`, STARTTLS, poort 587, met een
  [app-wachtwoord](https://myaccount.google.com/apppasswords) (daarvoor moet
  tweestapsverificatie aan staan);
- **Outlook.com en Microsoft 365** werken niet: Microsoft staat inloggen met
  een wachtwoord via SMTP niet meer toe;
- *Beveiliging* is STARTTLS (587), TLS (465) of geen; kies geen alleen voor
  een mailserver in je eigen netwerk. Met *geen* kun je niet inloggen (het
  wachtwoord zou onversleuteld gaan), behalve bij een mailserver op de
  gateway zelf (`localhost`); laat gebruikersnaam en wachtwoord dan leeg.

Het wachtwoord staat versleuteld in de database (met `secret.key` uit de
datamap, net als de wachtwoorden van plugins) en de API geeft het nooit
terug. Met *Testmail sturen* kijk je of het werkt.

**Zonder gateway**: `hwlocal report` maakt een rapport uit de datamap, ook
als de gateway draait:

```bash
hwlocal report -data /pad/naar/data -month 2026-10
hwlocal report -data /pad/naar/data -from 2026-01-01 -to 2026-12-31 -sections summary,costs -o jaar.pdf
```

Zonder `-month` of `-from`/`-to` is het de vorige maand. De simulator
(`hwsim`) heeft een nep-mailserver op `127.0.0.1:18025` (beveiliging *geen*,
elke login is goed) die ontvangen mail als `.eml` in `data/hwsim-mail`
zet.

## Back-up en terugzetten

De gateway maakt elke dag een back-up van de datamap: één zip met een
consistente kopie van de database (gemaakt terwijl de gateway gewoon
doordraait) en de bestanden ernaast: `secret.key` (zonder die sleutel laden
de plugin- en mailinstellingen leeg), de sleutel voor pushmeldingen,
`auth.json` (wachtwoord en API-tokens), de instellingen voor toegang en
tabbladen, het ENTSO-E-token en certificaten. Logbestanden, de
`.bak`-kopieën van vóór een migratie en eerdere back-ups gaan er niet in.

- Standaard om 03:30 naar `<datamap>/backups`, en de laatste 7 blijven
  bewaard (`HWLOCAL_BACKUP_TIME`, `HWLOCAL_BACKUP_DIR`,
  `HWLOCAL_BACKUP_KEEP`). `HWLOCAL_BACKUP_DIR=off` zet ze uit.
- Een back-up op dezelfde schijf helpt tegen een beschadigde database of
  een vergissing, niet tegen een kapotte schijf: zet de map bij voorkeur op
  een andere schijf of een NAS.
- Stond de gateway uit op het tijdstip en is de nieuwste back-up ouder dan
  een dag, dan maakt hij er twee minuten na de start een.
- Lukt een back-up niet, dan staat dat in het log en bij **Instellingen →
  Back-up**, en krijgen de apps met meldingen aan een pushmelding. Daar zie
  je ook de bewaarde back-ups en maak je er meteen een met *Nu back-up
  maken*.
- Een back-up bevat de sleutel en alle wachtwoorden, met rechten `600`. De
  app kan hem daarom niet downloaden en de map niet wijzigen; dat gaat
  alleen via de configuratie.
- Windows-service: die draait als LocalSystem en kan niet op een
  netwerkschijf schrijven die met jouw account gekoppeld is. Gebruik een
  lokale map (bijvoorbeeld een tweede schijf), of een UNC-pad
  (`\\nas\back-up\hwlocal`) waar het computeraccount schrijfrecht heeft.
  Zet de map met `hwlocal service install -backup-dir D:\hwlocal-backup`.

**Terugzetten.** Stop eerst de gateway of de service, en zet de back-up dan
terug in de datamap:

```bash
hwlocal restore -data /var/lib/hwlocal /pad/naar/hwlocal-backup-20261117-0330.zip
```

Heeft de datamap al een database, dan is `-force` nodig; de bestanden die
vervangen worden gaan dan naar `pre-restore-<tijd>` in de datamap. Een
back-up van een nieuwere versie (hogere schemaversie) wordt geweigerd; een
oudere wordt bij de volgende start bijgewerkt. Zo verhuis je de gateway ook
naar een andere computer, met alle instellingen en wachtwoorden.

## Installeren

Bouw één bestand voor het doelapparaat (vanaf elke pc):

```powershell
$env:CGO_ENABLED="0"; $env:GOOS="linux"; $env:GOARCH="arm64"; go build -o dist/hwlocal ./cmd/hwlocal
```

| Doel | `GOOS` | `GOARCH` |
|---|---|---|
| Raspberry Pi 3/4/5, 64-bit OS | `linux` | `arm64` |
| Raspberry Pi 32-bit OS | `linux` | `arm` (+ `GOARM=7`) |
| NAS / server (Intel/AMD) | `linux` | `amd64` |
| Windows | `windows` | `amd64` |

De GitHub Actions-workflow kan deze varianten bij elke push bouwen, maar
staat uit; bouw ze lokaal met het commando hierboven.

**Raspberry Pi / Linux (systemd):** zie de stappen bovenin
[`deploy/hwlocal.service`](../deploy/hwlocal.service).

**NAS met Docker:** `docker compose -f deploy/docker-compose.yml up -d`. De
container draait met `network_mode: host`, anders werkt mDNS niet.

**Windows (installer):** start `hwlocal-setup-<versie>.exe`. Die bouw je
met `installer\build.ps1` (zie hieronder), of je haalt hem uit GitHub
Releases als daar een versie staat.
De installer vraagt de HTTP-poort en of je HTTPS met een eigen certificaat
wilt (en controleert of die poorten vrij zijn), en installeert hwlocal als
Windows-dienst *Home Energy Watch* (dienstnaam `hwlocal`): start
mee met de pc (ook zonder dat iemand is ingelogd) en herstart bij een fout.

| | Waar |
|---|---|
| Programma | `C:\Program Files\hwlocal\hwlocal.exe` |
| Gegevens (database, tokens, certificaten) | `C:\ProgramData\hwlocal` (alleen SYSTEM, administrators en de dienst) |
| Log | `C:\ProgramData\hwlocal\hwlocal.log` (max. 10 MB, plus `hwlocal.log.1`) |
| Snelkoppeling | Startmenu → *Home Energy Watch* (een upgrade hernoemt de oude snelkoppeling *HomeWizard Local*) |

- **Bijwerken:** start de installer van de nieuwe versie. Die stopt de
  dienst, vervangt het programma en start de dienst met de bestaande
  instellingen. Is het databaseschema veranderd, dan maakt hwlocal bij de
  start eerst een kopie (`hwlocal.db.v<oud schema>.bak`) en werkt daarna de
  database bij. Ging er iets mis: dienst stoppen, `hwlocal.db` vervangen door
  die kopie en de vorige versie installeren. Een oudere versie weigert een
  database met een nieuwer schema.
- **Verwijderen:** via *Instellingen → Apps*. De gegevens blijven staan,
  tenzij je bij het verwijderen kiest om ze ook te wissen.
- De firewallregel geldt voor privé- en domeinnetwerken. Staat je netwerk in
  Windows op *openbaar*, zet het dan op *privé*, anders kan je telefoon de
  gateway niet bereiken.
- Instellingen later wijzigen: in een opdrachtprompt als administrator
  `"C:\Program Files\hwlocal\hwlocal.exe" service install <vlaggen>` (zie
  `hwlocal -h`, bijv. `-tls selfsigned -http :80`). Omgevingsvariabelen
  gelden niet voor de dienst.
- Al gegevens van een eerdere handmatige start (map `data` naast
  `hwlocal.exe`)? Stop de dienst (`hwlocal service stop`), kopieer de inhoud
  naar `C:\ProgramData\hwlocal` en start hem weer (`hwlocal service start`).

**Fouten terugvinden.** Alles staat in `hwlocal.log` (of `hwlocal.log.1`
als de log net is doorgeschoven):

- Een crash van de gateway zelf (een *panic*): de regel `panic: ...` met
  daaronder de stacktrace. Daarna start Windows de dienst opnieuw; zoek dus
  op `panic` vlak voor een nieuwe start.
- Een fout in een verzoek aan de webserver die de gateway opving:
  `level=ERROR` met `http: panic serving ...`. De gateway draait gewoon door.
- Een fout in de app (PWA) in een browser of op een telefoon:
  `level=WARN msg="fout in PWA"` met de melding, het bestand en de regel, de
  pagina, het adres en de browser, en de stacktrace. Per pagina-load worden
  hooguit 10 verschillende fouten gestuurd, en de gateway logt er hooguit 30
  per minuut (wat erboven valt wordt geteld). Met een wachtwoord komen
  alleen fouten van ingelogde browsers door.

Zoeken in PowerShell:
`Select-String -Path C:\ProgramData\hwlocal\hwlocal.log* -Pattern 'panic|fout in PWA|level=ERROR'`.
Start je hwlocal zelf in een venster of onder systemd, dan staat dit
allemaal op de uitvoer (bij systemd: `journalctl -u hwlocal`).

Zonder installer kan het ook: `hwlocal.exe` starten om het te proberen, of
in een opdrachtprompt als administrator `hwlocal.exe service install`
(kopieert zichzelf naar Program Files en doet hetzelfde als de installer;
daarnaast `service status`, `start`, `stop` en `uninstall`).

De installer zelf bouwen: Inno Setup 6 installeren
(`winget install JRSoftware.InnoSetup`) en `installer\build.ps1` uitvoeren;
resultaat in `dist\`. Testen zonder de eigen pc te veranderen:
`installer\sandbox\run.ps1` installeert, werkt bij en verwijdert in Windows
Sandbox (logs in `dist\sandbox-out`); met `-Interactive` klik je de wizard
daar zelf door.

**Bijwerken op afstand.** Vanaf je ontwikkel-pc een nieuwe build op een
andere Windows-pc zetten waar de dienst draait, zonder installer:

```bash
scripts/deploy-windows.sh gebruiker@192.168.1.50
```

Dat bouwt de uitgecheckte code (versie zoals hieronder), kopieert hem via
SSH, stopt de dienst, vervangt `hwlocal.exe` (de vorige blijft staan als
`hwlocal.exe.old`), start de dienst en toont de log van de nieuwe start
plus een controle van de webserver. Start de nieuwe versie niet, dan zet het
script de vorige terug, behalve als de nieuwe de database al had bijgewerkt;
dan zegt het hoe je dat met de `.bak`-kopie terugdraait. De instellingen van
de dienst blijven zoals ze waren. Bij Windows *Apps* blijft de versie van de
laatste installer staan; de echte versie zie je onder **Instellingen → Gateway**.
Met een tweede argument zet je een bestaande exe neer in plaats van te bouwen.

Eenmalig nodig op de doel-pc (PowerShell als administrator), met een
account dat administrator is:

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Start-Service sshd; Set-Service sshd -StartupType Automatic
```

En inloggen met een sleutel: maak op je ontwikkel-pc
`ssh-keygen -t ed25519 -f ~/.ssh/hwlocal_deploy` (het script gebruikt die,
of `HWLOCAL_DEPLOY_KEY`) en zet de regel uit `hwlocal_deploy.pub` op de
doel-pc in `C:\ProgramData\ssh\administrators_authorized_keys` (voor
administrators kijkt Windows daar, niet in `~\.ssh`). Dat bestand mag alleen
voor administrators en SYSTEM zijn, anders negeert sshd het:

```powershell
icacls "C:\ProgramData\ssh\administrators_authorized_keys" /inheritance:r /grant "*S-1-5-32-544:F" /grant "*S-1-5-18:F"
```

Test met `ssh -i ~/.ssh/hwlocal_deploy gebruiker@192.168.1.50 "sc query hwlocal"`.

**Versies.** Elke merge naar de hoofdbranch is een nieuwe versie
`v0.1.N`, waarbij N het aantal versies op de hoofdbranch is. Builds van een
andere branch heten `0.1.N-dev.<commit>`: de versie die het na het mergen
wordt. Met niet-gecommitte wijzigingen komt er `-dirty` achter. Je ziet de
versie onder **Instellingen → Gateway**, bij `hwlocal -version`, in de naam
van de installer en bij Windows *Apps*. `installer\build.ps1` bepaalt de
versie zelf; `scripts/version.sh` doet hetzelfde voor de GitHub
Actions-workflow. Die workflow staat uit: er komt dus geen automatische
GitHub Release, en de installer bouw je na een merge lokaal. Zet je de
workflow weer aan, dan maakt die bij elke merge een release met de
installer en de builds voor alle platformen.

## HTTPS en installeren als app

Een PWA installeren (beginscherm, offline openen) kan alleen via HTTPS. Drie
standen via `HWLOCAL_TLS`:

| Stand | Wat je krijgt | Wat je doet |
|---|---|---|
| `off` (standaard) | gewone website via http | niets |
| `acme` | geldig Let's Encrypt-certificaat, geen waarschuwingen | domein + Cloudflare-token |
| `selfsigned` | eigen certificaat van de gateway | het CA-certificaat eenmalig op elk apparaat installeren (`/ca.crt`) |

### HTTPS via Tailscale

Heb je [Tailscale](https://tailscale.com) op de gateway, dan geeft
`tailscale serve` de app een geldig https-adres zonder certificaat te
regelen. Nodig voor pushmeldingen en voor installeren als app.

1. Zet in de Tailscale-beheerconsole onder **DNS** *MagicDNS* en *HTTPS
   Certificates* aan.
2. Open de app vanaf het lokale netwerk en kies onder **Instellingen →
   Toegang op afstand** bij *Tailscale Serve* de poort waarop Tailscale de
   app aanbiedt, en klik **Instellen**. 443 geeft een adres zonder
   poortnummer (`https://gateway-pc.tail1234.ts.net`); gebruikt een andere
   app op deze computer 443 al via Tailscale Serve, kies dan bijvoorbeeld 8443
   (`https://gateway-pc.tail1234.ts.net:8443`).

   De gateway controleert eerst of de poort vrij is: niet al in gebruik door
   een andere app in Tailscale Serve (die worden onder het veld getoond) en
   niet door een ander programma op deze computer. Daarna voert hij
   `tailscale serve --bg --https=<poort> http://127.0.0.1:<poort van de
   gateway>` uit en zet hij de MagicDNS-naam bij de extra hostnamen. Dit
   blijft actief na een herstart. Kies je later een andere poort, dan haalt
   hij de oude weg; **Uitzetten** haalt alleen de poorten van de gateway weg,
   die van andere apps blijven staan.
3. Open het https-adres op je telefoon (met Tailscale aan), installeer de app
   en zet onder **Meldingen** meldingen aan.

De gateway zoekt het `tailscale`-programma in `PATH` en op de standaardplek
van de installer. Op Linux mag een gewone gebruiker `tailscale serve` alleen
als operator: `sudo tailscale set --operator=<gebruiker van de gateway>`.
Op Windows neemt Tailscale alleen opdrachten aan van de Windows-gebruiker die
het gebruikt; de gateway als service krijgt dan "Tailscale already in use by
…", ook met *Run unattended* aan. De app legt dat uit; stel Serve dan met
de hand in vanuit dat account (zie hieronder).
Met `HWLOCAL_TLS` aan stuurt Tailscale door naar de https-poort van de
gateway (`https+insecure://`, het verkeer blijft op deze computer).

Met de hand kan het ook: `tailscale serve --bg --https=8443 8080`, en zet de
naam dan zelf bij de extra hostnamen (of in `HWLOCAL_ALLOWED_HOSTS`); anders
weigert de gateway hem (HTTP 421).

`tailscale serve` stuurt het verkeer via `127.0.0.1` door, dus de schakelaar
*Toegang via Tailscale* is hiervoor niet nodig. Alleen apparaten in je
tailnet kunnen het adres openen. Instellen kan alleen vanaf het lokale
netwerk of via het Serve-adres zelf, niet via een Tailscale-IP-adres;
uitzetten kan altijd.
*Tailscale Funnel* (open voor heel internet) weigert de gateway, tenzij
`HWLOCAL_ALLOW_PUBLIC` aan staat.

### Eigen domein met Let's Encrypt via Cloudflare

De gateway haalt zelf een certificaat op via een **DNS-01-challenge**: hij
zet tijdelijk een TXT-record bij Cloudflare. De gateway hoeft daardoor nooit
vanaf internet bereikbaar te zijn; er gaat geen poort open op je router.

1. **DNS-record:** maak in Cloudflare een `A`-record, bijvoorbeeld
   `energie` → `192.168.1.20` (het vaste LAN-IP van de gateway), met
   proxy **uit** (grijze wolk, *DNS only*). Geef de gateway in je router een
   vast IP-adres.
2. **API-token:** Cloudflare → *My Profile → API Tokens → Create Token →
   Edit zone DNS*, beperkt tot jouw zone.
3. **Starten:**

   ```powershell
   $env:HWLOCAL_DOMAIN = "energie.jouwdomein.nl"
   $env:HWLOCAL_ACME_EMAIL = "jij@jouwdomein.nl"
   $env:CLOUDFLARE_API_TOKEN = "<token>"
   $env:HWLOCAL_HTTPS_ADDR = ":443"
   go run ./cmd/hwlocal
   ```

   Test eerst met `HWLOCAL_ACME_STAGING=true` (testcertificaat, geen
   limieten); haal daarna `data/certmagic` weg en start zonder staging.
   Vernieuwen gaat automatisch.
4. Open `https://energie.jouwdomein.nl` op je telefoon en kies
   *Toevoegen aan beginscherm* (iPhone: deelknop; Android: menu → *App
   installeren*).

**Let op DNS-rebind-bescherming.** Sommige routers (bijvoorbeeld FRITZ!Box)
blokkeren DNS-antwoorden met een privé-IP. Zet dan je domein op de
uitzonderingslijst (FRITZ!Box: *Thuisnetwerk → Netwerk → Netwerkinstellingen
→ DNS-rebind-beveiliging*).

Buiten je netwerk wijst het domein naar een adres dat daar niet bestaat. De
app opent dan wel, maar meldt dat er geen verbinding is.

## Configuratie

Alles via omgevingsvariabelen (of dezelfde opties als vlag, zie
`hwlocal -h`).

| Variabele | Standaard | Betekenis |
|---|---|---|
| `HWLOCAL_INTERVAL` | `2s` | uitleesinterval per apparaat (1s–60s) |
| `HWLOCAL_DATA_DIR` | `data` | database, tokens en certificaten |
| `HWLOCAL_HTTP_ADDR` | `:8080` | HTTP-adres; met TLS aan stuurt dit door naar HTTPS |
| `HWLOCAL_HTTPS_ADDR` | `:8443` | HTTPS-adres als TLS aan staat |
| `HWLOCAL_TLS` | `off`, of `acme` als er een domein is | `off`, `acme`, `selfsigned` |
| `HWLOCAL_DOMAIN` | | domein voor Let's Encrypt |
| `HWLOCAL_ACME_EMAIL` | | e-mail voor Let's Encrypt |
| `HWLOCAL_ACME_STAGING` | `false` | Let's Encrypt-testomgeving |
| `CLOUDFLARE_API_TOKEN` | | token met *Zone.DNS:Edit* |
| `HWLOCAL_TLS_NAMES` | | extra namen/IP's voor `selfsigned` |
| `HWLOCAL_DISCOVERY` | `true` | automatisch zoeken via mDNS |
| `HWLOCAL_DISCOVERY_INTERVAL` | `5m` | hoe vaak opnieuw zoeken |
| `HWLOCAL_DEVICES` | | IP's om bij de start toe te voegen |
| `HWLOCAL_RETENTION_DAYS` | `400` | bewaartermijn minuutdata; kwartierdata blijft altijd |
| `HWLOCAL_TZ` | `Europe/Amsterdam` | tijdzone voor dag- en maandtotalen |
| `HWLOCAL_ALLOW_PUBLIC` | `false` | ook clients van buiten het LAN toestaan |
| `HWLOCAL_ALLOW_TAILSCALE` | `false` | Tailscale-adressen (`100.64.0.0/10`) altijd toestaan; kan ook in de app, zie Beveiliging |
| `HWLOCAL_ALLOWED_HOSTS` | | extra hostnamen waarop je de app opent (kommagescheiden, of `*`); kan ook in de app, zie Beveiliging |
| `HWLOCAL_PUSH_CONTACT` | `https://github.com/bytebanger-nl/homeenergywatch` | contact voor de pushdiensten (`mailto:` of `https:`), meegestuurd met elke melding |
| `HWLOCAL_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `HWLOCAL_PRICE_SOURCES` | `energyzero,nordpool,entsoe` | stroomprijsbronnen in volgorde, of `off` |
| `HWLOCAL_GAS_PRICE_SOURCES` | `energyzero` | gasprijsbron, of `off` |
| `ENTSOE_API_TOKEN` | | token voor ENTSO-E; anders `entsoe-token.txt` in de datamap (zie Prijsbronnen) |
| `HWLOCAL_PRICE_BACKFILL_FROM` | `2026-01-01` | ontbrekende prijzen vanaf deze dag ophalen, of `off` |
| `HWLOCAL_BACKUP_DIR` | `<datamap>/backups` | map voor de dagelijkse back-up, of `off` (zie Back-up en terugzetten) |
| `HWLOCAL_BACKUP_TIME` | `03:30` | tijdstip van de back-up (UU:MM, in `HWLOCAL_TZ`) |
| `HWLOCAL_BACKUP_KEEP` | `7` | aantal back-ups dat bewaard blijft |
| `HWLOCAL_DEVICE_CA` | | extra CA voor apparaten (alleen voor de simulator) |
| `HWLOCAL_NORDPOOL_URL` | | ander adres voor Nord Pool (alleen voor de simulator) |
| `HWLOCAL_ENERGYZERO_URL` | | ander adres voor EnergyZero (alleen voor de simulator) |
| `HWLOCAL_TADO_URL` | | ander adres voor tado, inloggen en API (alleen voor de nep-tado van de simulator; tado wordt dan elke 10 s uitgelezen) |
| `HWLOCAL_ISTA_URL` | | ander adres voor mijn.ista.nl (alleen voor de simulator) |
| `HWLOCAL_ISTA_PACE` | `700ms` | pauze tussen verzoeken aan ista bij het ophalen van historie (`-1ns`: geen, alleen voor de simulator) |

## Beveiliging

- De gateway beantwoordt standaard alleen verzoeken van privé-adressen
  (LAN, loopback). Zet hem nooit open naar internet.
- Verzoeken vanaf andere websites (CSRF) worden geweigerd, zodat een website
  in je browser niet stiekem een stekker kan schakelen.
- Alleen bekende hostnamen worden beantwoord (tegen *DNS rebinding*, waarbij
  een website zijn eigen domein naar het adres van de gateway laat wijzen):
  IP-adressen, `localhost`, namen zonder punt en thuisnetwerknamen
  (`.local`, `.lan`, `.home.arpa`, `.fritz.box`, …), het Let's
  Encrypt-domein, `HWLOCAL_TLS_NAMES` en de computernaam. Open je de app via
  een andere naam, zet die dan bij **Instellingen → Toegang op afstand** of in
  `HWLOCAL_ALLOWED_HOSTS`; anders krijg je "onbekende hostnaam" (HTTP 421).
- Zonder wachtwoord kan iedereen in je netwerk de app gebruiken, apparaten
  bedienen en instellingen wijzigen. Heb je gasten op je wifi, apparaten die
  je niet vertrouwt of toegang via Tailscale, stel dan een wachtwoord in (zie
  hieronder).
- Op afstand: gebruik [Tailscale](https://tailscale.com) in plaats van een
  poort in je router open te zetten. Installeer het op de gateway en op je
  telefoon/laptop en zet in de app bij **Instellingen → Toegang op afstand**
  de schakelaar *Toegang via Tailscale* aan (adressen in `100.64.0.0/10`;
  standaard uit omdat providers die ook voor CGNAT gebruiken). Dat werkt
  meteen, zonder herstart of herinstallatie, en wordt bewaard in
  `access.json` in de datamap. Aanzetten en namen toevoegen kan alleen vanaf
  het lokale netwerk of de computer zelf; via Tailscale kun je het alleen
  uitzetten. `HWLOCAL_ALLOW_TAILSCALE=true` (`--allow-tailscale`) zet het
  vast aan; de schakelaar kan het dan niet uitzetten. Open de app via
  `http://<computernaam>:8080` of het Tailscale-IP dat de app toont; de lange
  MagicDNS-naam (`<computernaam>.<tailnet>.ts.net`) zet je in hetzelfde
  scherm (of in `HWLOCAL_ALLOWED_HOSTS`). Een tunnel die op dezelfde machine eindigt
  (bijvoorbeeld Cloudflare Tunnel) komt binnen via `127.0.0.1` en wordt dus
  altijd toegelaten: zet daar altijd een inlog voor (Cloudflare Access).
- Tokens van API v2-apparaten staan alleen in `data/hwlocal.db` (rechten
  `600`, ook de bijbehorende `-wal`- en `-shm`-bestanden) en worden nooit via
  de API teruggegeven of gelogd. Een apparaatcertificaat moet geldig zijn en
  door de HomeWizard-CA ondertekend; anders wordt er geen token verstuurd.
- Wachtwoorden en sleutels van plugins (het ista-wachtwoord, het Tuya-secret
  en de local keys) staan **versleuteld** (AES-256-GCM) in de database, met
  een sleutel in `secret.key` in de datamap. Zo staan ze niet leesbaar in de
  database of in de `.bak`-kopieën ervan. Wie bij de hele datamap kan, of
  bij een back-up (daar zit `secret.key` in), kan ze wel ontsleutelen. Zonder `secret.key` (een
  database naar een andere computer gekopieerd) moet je die wachtwoorden
  opnieuw invullen.

### Wachtwoord en API-tokens

Standaard heeft de app geen wachtwoord, zoals altijd. Zet je er een aan, dan
vragen de app en de hele API (`/api/…`) om in te loggen; alleen de bestanden
van de app zelf (om het inlogscherm te tonen) en `/ca.crt` blijven open.

- **Instellen**: in de app bij **Instellingen → Beveiliging**. Zolang er geen
  wachtwoord is, mag iedereen er een instellen (zonder wachtwoord mag immers
  alles); de browser waarmee je dat doet blijft meteen ingelogd. Minstens 8
  tekens. Op de gateway zelf kan het ook: `hwlocal password` (vraagt twee keer
  om het nieuwe wachtwoord; `-data` voor een andere datamap, standaard
  `HWLOCAL_DATA_DIR`, `./data` of bij de Windows-dienst
  `C:\ProgramData\hwlocal`; daar in een opdrachtprompt als beheerder, op Linux
  met `sudo`).
- **Inloggen**: elke browser en elke geïnstalleerde app (ook op je telefoon)
  logt één keer in. Hij blijft ingelogd zolang je hem gebruikt: een sessie
  vervalt pas na 90 dagen zonder gebruik. Het sessiecookie is `HttpOnly` en
  `SameSite=Strict` (en `Secure` onder https), dus scripts op een pagina en
  andere websites kunnen er niet bij.
- **Wijzigen**: met je huidige wachtwoord. Alle andere browsers moeten daarna
  opnieuw inloggen; een open app merkt dat binnen 30 seconden. Bij
  **Ingelogde browsers** zie je welke browsers ingelogd zijn (browser,
  IP-adres, laatst gebruikt) en log je er een of alle andere uit.
- **Vergeten**: op de gateway zelf `hwlocal password -off` (weer zonder
  wachtwoord) of `hwlocal password` (nieuw wachtwoord). Een draaiende gateway
  neemt dat meteen over, een herstart is niet nodig.
- **Uitzetten**: in de app met je huidige wachtwoord; de app is daarna weer
  open voor iedereen op je netwerk.
- **Te veel pogingen**: na 5 onjuiste wachtwoorden vanaf hetzelfde adres
  wacht de gateway 1 minuut, en bij elke volgende fout twee keer zo lang (tot
  30 minuten). Een goed wachtwoord zet de teller terug.
- **API-tokens** voor scripts en koppelingen (bijvoorbeeld Home Assistant)
  maak je bij **API-tokens** op dezelfde plek. Een token zie je één keer;
  stuur het mee als header: `curl -H "Authorization: Bearer hwl_…"
  http://gateway:8080/api/devices`. *Alleen lezen* mag alleen `GET`
  (ook `/api/stream`); *Lezen en bedienen* mag alles behalve de
  beveiliging zelf (wachtwoord, sessies en tokens). Tokens blijven geldig na
  een nieuw wachtwoord; trek ze in als je ze niet meer gebruikt.
- **https**: over gewone http gaat het wachtwoord onversleuteld over je
  netwerk. Dat houdt nieuwsgierige gebruikers buiten, maar niet iemand die je
  wifi afluistert. Gebruik voor echte bescherming https (`tailscale serve`,
  `HWLOCAL_TLS=selfsigned` of een eigen domein, zie *HTTPS en installeren als
  app*).
- Alles staat in `auth.json` in de datamap (rechten `600`): het wachtwoord
  als PBKDF2-hash (SHA-256, 600.000 rondes), sessies en tokens alleen als
  SHA-256 van hun geheim. Wie dat bestand leest, kan er dus niet mee inloggen.
  Is het bestand beschadigd, dan start de gateway niet (in plaats van open
  te gaan); `hwlocal password -off` lost dat op.

## Projectindeling

| Map | Inhoud |
|---|---|
| `cmd/hwlocal` | de gateway (configuratie, HTTP(S)-servers) |
| `cmd/hwsim` | simulator met nep-apparaten |
| `internal/homewizard` | client voor API v1 en v2, normalisatie, HomeWizard-CA |
| `internal/gateway` | apparaatbeheer, uitlezen, koppelen, live-events |
| `internal/history` | minuutaggregatie, kwartierberekening, historievragen |
| `internal/importer` | import van CSV-exports uit de HomeWizard-app |
| `internal/costs` | contracten en kostenberekening per dag, met dagcache |
| `internal/prices` | day-ahead-stroomprijzen en gasdagprijzen (EnergyZero, Nord Pool, ENTSO-E) |
| `internal/ista` | verwarming via mijn.ista.nl: inloggen, ophalen, verbruik per kamer, kosten per afrekenperiode |
| `internal/store` | SQLite (pure Go, geen C-compiler nodig) |
| `internal/discovery` | mDNS (`_hwenergy._tcp`, `_homewizard._tcp`) |
| `internal/rules` | regels voor meldingen en schakelen, verzenden naar de pushabonnementen |
| `internal/webpush` | Web Push: versleuteling (RFC 8291) en VAPID (RFC 8292) |
| `internal/server` | JSON-API, server-sent events, PWA-bestanden |
| `internal/tlsconfig` | Let's Encrypt (Cloudflare) en eigen CA |
| `web/static` | de PWA |
| `docs/` | ontwerpnotities, o.a. [energiecontracten](CONTRACTS.md) en [ista](ISTA.md) |

### API

| Methode en pad | |
|---|---|
| `GET /api/info` | versie, interval, TLS-stand |
| `GET /api/devices` | alle apparaten met laatste meting en vandaag-totalen |
| `GET /api/stream` | server-sent events: `snapshot` (met `controls`: de lopende en recente opdrachten), daarna onder meer `device` / `removed` / `plugin` / `control` (de voortgang van een opdracht) |
| `POST /api/devices` `{"host"}` | apparaat toevoegen op IP |
| `GET /api/outages?device&days=30` | wanneer een apparaat (HomeWizard of plugin; zonder `device` alle apparaten) niet bereikbaar was in de laatste `days` dagen, nieuwste eerst: `count`, `unread`, `total_s`, `to` en `outages` (`device_id`, `start`, `end` (leeg: nog steeds), `cut` (de gateway stopte tijdens de storing), `message`, `read`) |
| `POST /api/outages/read` `{"device","until"}` | storingen (van één apparaat of alle) die vóór `until` begonnen als gelezen markeren |
| `GET /api/notices?days=30` | berichten over wat misging (`kind`: `report`, `backup`, `control`, `login`, `plugin`; `title`, `text`, `first`, `last`, `count`, `read`), nieuwste eerst, met `unread` en `to` |
| `POST /api/notices/read` `{"until"}` | berichten die vóór `until` voor het laatst gebeurden als gelezen markeren |
| `GET /api/inbox` | wat ongelezen is, voor het belletje: `unread`, `outages`, `notices` en per apparaat `devices` (`unread`, `open`) |
| `PATCH /api/devices/{id}` `{"name","enabled","show_counters"}` | wijzigen (`show_counters`: meterstanden op het dashboard) |
| `DELETE /api/devices/{id}?purge=true` | verwijderen (optioneel met historie) |
| `POST /api/devices/{id}/pair` | koppelen via API v2 starten |
| `POST /api/devices/{id}/identify` | apparaat laten knipperen |
| `PUT /api/devices/{id}/power` `{"on"}` | Energy Socket schakelen: `202` met de status van de opdracht (zie [Bedienen](#bedienen)); `404` voor een onbekend apparaat, `400` als het niet geschakeld kan worden |
| `GET /api/history/rate?device&from&to&points` | vermogen (W) of waterflow (L/min) |
| `GET /api/history/energy?device&from&to&bucket` | energie per `quarter`/`hour`/`day`/`month` |
| `GET /api/history/grid?device&from&to&points` | spanning, stroom en vermogen per fase (`phases`, `points`; alleen binnen de bewaartermijn van de minuutgegevens) en netgebeurtenissen (`events`: `sag`, `swell`, `power_fail`, `long_power_fail`) |
| `POST /api/import?dry_run&device` (CSV als body) | historie importeren; `409` met `candidates` als het apparaat gekozen moet worden |
| `GET /api/prices?from&to` | marktprijzen stroom (`prices`, `eur_mwh`) en gas (`gas`, `eur_m3`), met contract ook wat je betaalt (`all_in_ct`, `all_in_eur_m3`) en het contracttype van die periode (`contract`: `dynamic`/`fixed`); standaard vandaag en morgen |
| `POST /api/prices/fetch?date&energy` | prijzen van een dag nu ophalen (`energy=gas` voor gas) |
| `GET /api/contracts` | alle contracten |
| `POST /api/contracts`, `PUT /api/contracts/{id}` | contract toevoegen of wijzigen |
| `DELETE /api/contracts/{id}` | contract verwijderen |
| `GET /api/costs?from&to&bucket&device` | kosten per `hour` (hooguit 8 dagen)/`day`/`month`/`year` (standaard de P1-meter) |
| `GET /api/costs?…&device&scope=usage` | alleen de stroomkosten van de afname van één tussenstekker of kWh-meter, tegen het tarief van dat moment; zonder vaste kosten, teruglevering en gas |
| `GET /api/scenarios`, `POST /api/scenarios`, `PUT/DELETE /api/scenarios/{id}` | scenario's ("wat als"-contracten) |
| `GET /api/compare?from&to&bucket&device` | werkelijke kosten naast die van elk scenario |
| `GET /api/advance?device` | voorschot tegenover kosten in de afrekenperiode, met prognose: `advances` heeft één status, of bij stroom en gas apart één per onderdeel (`part`: `electricity`/`gas`); `advance` is de eerste, `null` zonder voorschot |
| `/api/water/contracts`, `/api/water/costs`, `/api/water/advance` | hetzelfde voor water: eigen contracten, standaard de watermeter |
| `GET /api/rules` | regels met hun toestand (`armed`, `active_since`, `last_fired`, `last_result`; bij een handmatige actie `active` en `momentary` voor een knop); `regulated` geeft per regel-id wanneer die automatisering voor het laatst regelde |
| `POST /api/rules`, `PUT /api/rules/{id}` | regel toevoegen of wijzigen: `{"kind":"alert"\|"automation","name","enabled","match":"all"\|"any","conditions":[{"device","metric","op","value","scale","ref":{"device","metric","scale"}}],"for_s","message","actions":[…],"else_actions":[…],"manual"}`; `manual` (alleen bij een automatisering) maakt er een handmatige actie van, zonder voorwaarden; `op` is `<`, `<=`, `>`, `>=`, `=` of `!=`; acties `{"type":"notify"}`, `{"type":"switch","device","on"}`, `{"type":"plugin_set","device","key","value"}` (een melding heeft alleen `notify`); `device` `price` heeft `price`, `price_all_in`, `gas_price`, `gas_price_all_in`, `price_avg_today`, `price_cheaper_hours`; `today_*` is het verbruik van vandaag |
| `GET /api/rules/prices` | de huidige waarde van die prijzen (voor zover bekend) |
| `DELETE /api/rules/{id}` | regel verwijderen |
| `POST /api/rules/{id}/start`, `POST /api/rules/{id}/stop` | handmatige actie aan- of uitzetten; de gateway voert hem uit bij de volgende evaluatie (binnen `HWLOCAL_INTERVAL`) |
| `GET /api/rules/{id}/periods?from&to` | wanneer de automatisering regelde (`periods`: `start`, `end`; zonder `end` regelt ze nog), standaard de laatste 24 uur |
| `GET /api/dashboard/tiles` | de eigen dashboardtegels (`tiles`, met `max`); alleen met plugins |
| `POST /api/dashboard/tiles`, `PUT /api/dashboard/tiles/{id}` | tegel toevoegen of wijzigen: `{"name","size":"small"\|"wide","style":"value"\|"list"\|"grid"\|"big"\|"spark","items":[…]}`; onderdelen `{"type":"value","device","metric","label","target","show_device","show_trend","show_range"}`, `{"type":"status","device","metric","metric2","label"}`, `{"type":"chart","series":[{"device","metric"}],"hours":6\|24\|168,"label"}` of `{"type":"control","device","control"}`; `device` is een plugin-apparaat. Een handmatige actie: `{"type":"control","rule","label"}` met het id van de regel |
| `PUT /api/dashboard/tiles/order` `{"ids"}` | tegels in een nieuwe volgorde |
| `DELETE /api/dashboard/tiles/{id}` | tegel verwijderen |
| `GET /api/push` | publieke VAPID-sleutel en de apps met meldingen aan |
| `POST /api/push/subscriptions` | `PushSubscription.toJSON()` plus `label`; alleen https-adressen van pushdiensten op internet |
| `DELETE /api/push/subscriptions` `{"endpoint"}` | meldingen voor een app uitzetten |
| `POST /api/push/test` `{"endpoint"}` | testmelding naar één app, of zonder body naar alle |
| `GET /api/ista` | ista-accounts met hun stand van bijwerken (`accounts`: `state` `idle`/`syncing`/`auth_failed`/`error`, voortgang `done`/`todo`) en adressen met kamers en afrekenperiodes (`addresses`) |
| `POST /api/ista/sync` | ista nu bijwerken (als het aan de beurt is of nog niet klaar) |
| `GET /api/ista/usage?cuid&from&to&bucket` | verwarming per `day`/`week`/`month`/`period` (`from`, `to`: JJJJ-MM-DD, hooguit drie jaar): per kamer eenheden en tikken, totaal, dezelfde dagen vorig jaar, gebouwgemiddelde |
| `GET /api/ista/weeks?cuid&y` | weektabel van afrekenperiode `y` (zonder `y`: de lopende) |
| `GET /api/ista/costs?cuid&y` | kosten, prognose en voorschot van afrekenperiode `y` |
| `GET /api/ista/contracts?cuid`, `PUT /api/ista/contracts/{y}`, `DELETE /api/ista/contracts/{y}` | ista-contracten per afrekenperiode: `{"area_dm2","unit_price","area_price","correction_pct","estimate_units","advance":[{"from","amount"}],"final":{"variable_energy","fixed_energy","other","services","units_complex","area_complex_dm2","units","total"}}` |
| `GET /api/reports` | de onderdelen van een rapport (`sections`), de geplande rapporten (`schedules`, met `last_period`, `last_at`, `last_error`, `next_at`) en of e-mail is ingesteld (`mail_configured`) |
| `GET /api/reports/pdf?from&to&sections&title` | rapport als PDF over `from` t/m `to` (JJJJ-MM-DD, hooguit drie jaar), of met `range=contract_year`/`year` (in plaats van `from`/`to`) van het begin van het contractjaar of 1 januari t/m vandaag; `sections` kommagescheiden (`summary`, `electricity`, `gas`, `costs`, `water`, `heating`, `devices`, `scenarios`, `grid`, `advance`; standaard alle, in deze volgorde) |
| `POST /api/reports/schedules`, `PUT/DELETE /api/reports/schedules/{id}` | gepland rapport: `{"name","kind":"month"\|"week","range":""\|"contract_year"\|"year","sections":[…],"to":["a@b.nl"],"title","enabled"}`; in de lijst staat bij een rapport met alleen verwarming dat op ista wacht `wait_until`, en bij een rapport waarvan de verwarming nog volgt `heating_period` (de periode), `heating_until` (uiterlijk dan) en na een mislukte poging `heating_at` en `heating_error` |
| `POST /api/reports/schedules/{id}/send` | de laatste periode nu versturen |
| `GET /api/settings/mail` | de mailserver (`host`, `port`, `security`, `user`, `from`, `has_password`), nooit het wachtwoord |
| `PUT /api/settings/mail` | `{"host","port","security":"starttls"\|"tls"\|"none","user","password","from"}`; zonder `password` blijft het opgeslagen wachtwoord (bij dezelfde server en gebruiker), `clear_password` wist het; `host` leeg zet e-mail uit |
| `POST /api/settings/mail/test` `{"to"}` | testmail sturen |
| `GET /api/settings/entsoe` | of er een ENTSO-E-token is ingesteld (het token zelf nooit) |
| `GET /api/settings/forecast` | de percentages van de prognose: `settings` (eigen waarden; ontbrekend = automatisch), `defaults`, en per prognose (`used.electricity`, `.water`, `.heating`) de gebruikte percentages (`key`, `pct`, `source`: `computed`/`default`/`setting`, `auto` bij een eigen waarde, `months`) of waarom er geen prognose is (`error`) |
| `PUT /api/settings/forecast` | eigen waarden opslaan, bijv. `{"period_min_pct": 85, "trend": false, "gas_pct": 6}`; `{}` zet alles terug op automatisch; geeft hetzelfde als `GET` |
| `PUT /api/settings/entsoe` | `{"token": "..."}` instellen na controle bij ENTSO-E; `""` verwijdert het |
| `GET /api/settings/access` | toegang op afstand: Tailscale aan/uit, extra hostnamen, Tailscale-adressen van deze computer |
| `PUT /api/settings/access` | `{"tailscale": true, "hosts": ["pc.tail1234.ts.net"]}`; via Tailscale alleen beperken, niet uitbreiden |
| `GET /api/settings/tailscale-serve` | Tailscale Serve: beschikbaar (of `problem`), MagicDNS-naam, de https-poorten en adressen van deze gateway (`ports`, `urls`) en de poorten van andere apps (`used`) |
| `PUT /api/settings/tailscale-serve` | `{"port": 8443}`: controleert of de poort vrij is (anders 409), zet Tailscale Serve aan en verplaatst een eerdere poort van de gateway; niet via een Tailscale-IP-adres |
| `DELETE /api/settings/tailscale-serve` | haalt de Serve-poorten van deze gateway weg; die van andere apps blijven |
| `GET /api/settings/backup` | de back-up: `enabled`, `dir`, `at`, `keep`, `next`, de laatste poging sinds de start (`last`: `file`, `size`, `at`, `error`), de bewaarde back-ups (`backups`, nieuwste eerst) en `running`; een back-up zelf is nooit te downloaden |
| `POST /api/settings/backup` | nu een back-up maken; geeft hetzelfde als `GET`, `409` als back-ups uit staan |
| `GET /api/settings/tabs` | volgorde van de tabbladen: `{"order": [...]}`, leeg = standaard |
| `PUT /api/settings/tabs` | `{"order": ["dashboard", "tab:2", "verbruik", ...]}`: vaste tabbladen bij naam (`dashboard`, `verbruik`, `meldingen`, `automatisering`, `apparaten`, `klimaat`, `instellingen`), eigen tabbladen als `tab:<id>`; `[]` zet de standaardvolgorde terug. Alle open apps krijgen de nieuwe volgorde meteen (stream-event `tab_order`) |
| `POST /api/client-errors` | een fout uit de PWA naar de log: `{"kind": "error"\|"rejection", "message", "source", "line", "column", "stack", "page"}`; antwoord `204`, ook als de limiet van 30 per minuut is bereikt |
| `GET /api/auth` | of er een wachtwoord is (`enabled`) en of dit verzoek ingelogd is (`authenticated`, `session`); werkt zonder inloggen |
| `POST /api/auth/login` `{"password"}` | inloggen: zet het sessiecookie; `401` bij een onjuist wachtwoord, `429` bij te veel pogingen |
| `POST /api/auth/logout` | deze browser uitloggen |
| `PUT /api/auth/password` `{"current","password"}` | wachtwoord instellen (de eerste keer zonder `current`) of wijzigen; logt alle andere browsers uit |
| `DELETE /api/auth/password` `{"current"}` | wachtwoord uitzetten |
| `GET /api/auth/sessions`, `DELETE /api/auth/sessions/{id}` | ingelogde browsers (`current` is deze); `{id}` = `others` logt alle andere uit |
| `GET /api/auth/tokens`, `POST /api/auth/tokens` `{"name","scope"}`, `DELETE /api/auth/tokens/{id}` | API-tokens; `scope` is `read` of `control`; het geheim (`secret`) komt alleen in het antwoord op `POST` |

Met een wachtwoord geeft elk ander endpoint `401` (`{"error", "login": true}`)
zonder geldig sessiecookie of `Authorization: Bearer`-token. De endpoints
onder `/api/auth` (behalve `GET /api/auth`, inloggen en uitloggen) werken
alleen met een sessie, nooit met een token.

## Bedienen

Elke opdracht aan een apparaat gaat langs dezelfde weg, of hij nu uit de app
komt (een tegel, het Klimaat-scherm, de schakelaar van een tussenstekker) of
uit een automatisering, en of het nu een HomeWizard-tussenstekker is of een
plugin-apparaat (airco, tado, Tuya):

- de gateway stuurt de opdracht en kijkt of het apparaat de nieuwe stand ook
  toont (hooguit 30 seconden). Een tussenstekker meldt zijn stand bij elke
  meting en in zijn antwoord op de opdracht;
- is dat niet zo, of gaf het apparaat geen antwoord (offline, wifi-module
  die een opdracht laat vallen), dan stuurt hij hem na 15 seconden opnieuw,
  tot drie pogingen;
- een opdracht die niet kan (bediening uit, apparaat weg, waarde buiten het
  bereik, een vergrendelde tussenstekker) wordt niet herhaald; een opdracht
  zonder stand om te controleren (zoals *Terug naar schema*) alleen bij een
  fout;
- **de laatste opdracht wint**: een nieuwe opdracht voor dezelfde bediening
  van hetzelfde apparaat stopt een opdracht die nog loopt, ook tussen een
  automatisering en de app.

De app wacht daar niet op: hij krijgt de opdracht meteen terug
(`202`, `{"id","device","key","value","source","state","attempt","attempts","error","started","updated"}`)
en daarna de voortgang als stream-event `control`. `state` is `busy` (eerste
poging), `retry` (opnieuw), `ok`, `failed` of `replaced` (een nieuwere
opdracht nam het over); `source` is `app` of `rule`. Bij een plugin-apparaat
is `device` `plugin:<id>`. Onder de bediening staat dan *Bezig…*, *Lukt nog
niet, opnieuw proberen (poging 2 van 3)…* of in rood *Niet gelukt na 3
pogingen: …*; bij een opdracht van een automatisering staat
*Automatisering:* ervoor. Een mislukte opdracht blijft tien minuten zichtbaar
(of tot de volgende opdracht). De gateway gaat ook door als je de app
sluit; hooguit vijf minuten per opdracht.

## Bekende beperkingen

- Kosten: energiebelasting is één tarief per kWh/m³ (geen staffels boven
  10.000 kWh of 170.000 m³); de dagcache staat in het geheugen en wordt na
  een herstart opnieuw opgebouwd.
- Normaal/dal volgt de P1-meter alleen voor wat de gateway met deze versie
  mat of uit een HomeWizard-export importeerde. Oudere metingen, prognoses,
  het voorschot en de actuele prijs in regels gebruiken de klok (doordeweeks
  23–7 uur en weekend), dus daar telt een feestdag als normaal. T1 geldt als
  dal, zoals in Nederland; een Belgische meter (T1 = dag) wordt omgekeerd
  geteld.
- De Let's Encrypt-koppeling met Cloudflare is gebouwd volgens de documentatie
  van certmagic en libdns, maar nog niet getest tegen een echt domein.
- Import ondersteunt de kwartierexports van de HomeWizard-app; exports van
  netbeheerders of leveranciers nog niet.
- Gas wordt per uitleesmoment vastgelegd, niet op de tijdstempel van de
  gasmeter; kwartierwaarden voor gas kunnen daardoor tot 5 minuten (DSMR 5)
  of een uur (DSMR 4) verschuiven.
- Netgebeurtenissen: de slimme meter telt dips, pieken en storingen maar
  geeft geen tijdstip. hwlocal ziet alleen wanneer een teller oploopt
  (`since`..`at`); bij een stroomstoring valt ook de dongle uit, dus dat is
  ruwweg de duur van de storing. Grenzen van dips en pieken stelt de
  netbeheerder in de meter in (meestal 207 en 253 V). Per-fasewaarden vallen
  onder de bewaartermijn van minuutdata; gebeurtenissen blijven altijd.
- Wachtwoord: er is één wachtwoord voor de hele gateway, geen gebruikers of
  rechten per persoon (API-tokens kunnen wel alleen lezen). Achter
  `tailscale serve` of een tunnel komen alle verzoeken van `127.0.0.1`; de
  limiet op inlogpogingen telt dan per adres dat de proxy in
  `X-Forwarded-For` meegeeft (zonder die kop voor iedereen samen), en daarnaast
  telt alles via de proxy samen tot een ruimere grens (50 foute pogingen); de
  gateway weet niet dat de verbinding https is (het cookie krijgt dan geen
  `Secure`).
- Rapporten: klimaatapparaten en de thuisbatterij staan er nog niet in. De PDF gebruikt het standaardlettertype van PDF (Helvetica):
  tekens buiten West-Europees (Windows-1252), zoals emoji in een
  apparaatnaam, worden een vraagteken. Mail gaat alleen via een SMTP-server
  met gebruikersnaam en wachtwoord (geen inloggen via OAuth, dus geen
  Outlook.com of Microsoft 365).
- De WebSocket van API v2 wordt nog niet gebruikt. De laadtoestand komt van
  de batterijen zelf; de Thuisbatterij-plugin toont het gemiddelde van de
  batterijen die de gateway uitleest.
- Energiecontracten en kosten: zie [docs/CONTRACTS.md](CONTRACTS.md).
- Prognose: de trend en de berekende marge vergelijken deze periode met de
  vorige zonder rekening te houden met het weer; een koude winter vorig jaar
  maakt de gasprognose dus hoger. Zonder ingevuld jaarverbruik is er pas een
  prognose als de 12 maanden vóór de afrekenperiode voor 90% gemeten of
  geïmporteerd zijn.
- ista: er is geen officiële API; verandert ista de website, dan kan het
  ophalen stoppen. De standen lopen een paar dagen achter (de stand van
  vandaag is een tussenstand). Eenheden zijn een verdeelsleutel, geen energie:
  er is geen omrekening naar kWh. Getest tegen één account met alleen
  verwarming; warm water en andere diensten zijn wel voorzien maar niet gezien.
