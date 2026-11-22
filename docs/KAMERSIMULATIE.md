# Automatisering testen met de kamersimulatie

Met de kamersimulatie in `hwsim` probeer je een automatisering met een
wachtstap en regelen uit op je ontwikkel-pc. Zo zie je wat de regel doet
voordat hij je echte airco en tado bedient. Deze handleiding loopt het
voorbeeld *Verwarmen: eerst CV, dan airco* stap voor stap door; dat duurt
ongeveer een half uur.

## Wat er gesimuleerd wordt

- **Eén kamer** met één temperatuur:
  - de **CV** verwarmt zolang het doel van tado hoger is dan de
    kamertemperatuur, en houdt de kamer dan op dat doel;
  - de **airco** verwarmt, als hij aan staat op *Verwarmen*, de kamer tot
    zijn ingestelde temperatuur min 1,3 °C (hij meet aan het plafond, waar
    het warmer is) en houdt hem daar;
  - zonder warmtebron koelt de kamer langzaam af naar de buitentemperatuur.
- **Een nep-tado** met één kamer, *Woonkamer (sim)*. tado meet de
  kamertemperatuur en kent een handmatige instelling en *terug naar schema*.
  Het schema is één vaste temperatuur (standaard 18 °C).
- **Een MHI-airco** op `127.0.0.1:18551`. Hij start uitgeschakeld en toont
  dezelfde kamertemperatuur als binnentemperatuur.
- **Snellere tijd**: met `-speed 30` loopt de tijd in de kamer 30 keer zo
  snel, dus een uur kamertijd duurt 2 minuten. De tijden in de gateway
  (duur, wachtstap, regelen, tijdvenster) lopen op de gewone klok. Daarom
  maak je die in de test kort.

## 1. Starten

Je hebt Git Bash nodig (wordt met Git voor Windows geïnstalleerd). Open Git
Bash in de map van de repo en start:

```bash
HWSIM_ARGS="-speed 30 -room-start 17" scripts/dev-sim.sh data/kamersim
```

Dit doet het volgende:

- het bouwt `hwsim` en de gateway in `data/kamersim/bin`;
- het start `hwsim` op de achtergrond, met de opties uit `HWSIM_ARGS`
  (het log staat in `data/kamersim/hwsim.log`);
- het start de gateway in de voorgrond, met de nep-tado
  (`HWLOCAL_TADO_URL=http://127.0.0.1:18080/tado`). tado wordt dan elke 10
  seconden uitgelezen, in plaats van elk kwartier.

Open daarna <http://localhost:8080>.

Een paar aandachtspunten:

- **Draait `hwsim` al** (bijvoorbeeld zonder `-speed`), dan start het script
  geen nieuwe en gelden je opties niet. Stop dan eerst alles:
  `scripts/dev-sim.sh stop`.
- **Opnieuw beginnen**: stop alles en verwijder de map `data/kamersim`. Bij
  een volgende start voeg je de plugins en de automatisering opnieuw toe.
  Laat je de map staan, dan blijven de plugins en de automatisering bewaard.
  De kamer zelf begint wel elke keer opnieuw op `-room-start`.
- **Stoppen**: Ctrl+C in Git Bash stopt de gateway, en
  `scripts/dev-sim.sh stop` stopt ook `hwsim`.

### Opties van hwsim

| Optie | Standaard | Betekenis |
|---|---|---|
| `-speed` | 1 | zoveel keer sneller loopt de tijd in de kamer |
| `-room-start` | 17 | begintemperatuur van de kamer (°C) |
| `-outdoor` | 5 | buitentemperatuur (°C) |
| `-room-tau` | 4h | hoe snel de kamer afkoelt (tijdconstante, kamertijd) |
| `-heat-tau` | 45m | hoe snel CV en airco opwarmen (tijdconstante, kamertijd) |
| `-ac-offset` | 1,3 | de kamer wordt zoveel kouder dan de ingestelde temperatuur van de airco (°C) |
| `-tado-schedule` | 18 | temperatuur van het tado-schema (°C) |

Met een tijdconstante van 4 uur is na 4 uur kamertijd ongeveer twee derde
van het verschil met de buitentemperatuur weg.

Let op: het tado-schema staat op 18 °C. Bij een start op 17 °C warmt de CV
de kamer daarom eerst op tot 18 °C, ook als er nog geen automatisering is.

## 2. tado toevoegen

1. Ga naar *Instellingen → Plugins*.
2. Kies bij *Plugin toevoegen* **tado° verwarming** en klik *Toevoegen*.
3. Klik **Verbinden met tado**. De nep-tado keurt het inloggen na ongeveer
   5 seconden zelf goed; je hoeft geen link te openen. Er komt *✓ Verbonden
   met tado.* te staan.
4. Klik **Kamers ophalen** en voeg **Woonkamer (sim)** toe.
5. Vink **Bediening toestaan** aan en bevestig de vraag die verschijnt.

## 3. Airco toevoegen

1. Kies bij *Plugin toevoegen* **Mitsubishi Heavy (WF-RAC)** en klik
   *Toevoegen*.
2. Klik **Apparaat handmatig**. Vul een naam in (bijvoorbeeld *Airco
   (sim)*) en het IP-adres `127.0.0.1:18551`, en klik *Toevoegen*.
3. Vink **Bediening toestaan** aan en bevestig.

Controleer op het tabblad **Klimaat Plugins** of beide apparaten *Online*
zijn, en of de *Kamer* van de airco gelijk is aan de *Temperatuur* van
tado.

## 4. De automatisering maken

1. Ga naar **Automatisering** en klik onder *Voorbeelden* op **Verwarmen:
   eerst CV, dan airco**. Het formulier is dan al ingevuld.
2. Pas drie dingen aan, zodat je niet uren hoeft te wachten:
   - **Tijdvenster** (de eerste voorwaarde): *van* een tijd die al voorbij
     is, *tot* ongeveer een half uur vanaf nu. Om 21:20 bijvoorbeeld *van
     21:00 tot 21:55*. Aan het eind zie je dan ook dat het regelen stopt.
   - **Wachtstap**: *hooguit* **1** minuten (in plaats van 30).
   - **Regelen**: open **Meer instellingen** en zet *elke* op **1**
     minuten (in plaats van 15).
3. Laat de rest zoals het is:
   - als: binnen het tijdvenster, tado-temperatuur lager dan 18,5 °C en
     airco *Aan* is nee;
   - dan: tado *Temperatuur 19 °C* → wachten tot tado-temperatuur minstens
     19 °C → tado *Stand Schema*, airco *Modus Verwarmen*, *Aan*,
     *Temperatuur 21 °C*;
   - regelen: airco-temperatuur zodat tado 19 ± 0,2 °C blijft, airco op
     doel + 2, stappen van 0,5 °C tussen 18 en 23 °C, stoppen als de airco
     uit gaat of als het tijdvenster eindigt, en dan uitzetten.
4. Klik **Opslaan**. Een nieuwe automatisering kijkt meteen naar de
   situatie van dat moment, dus hij gaat direct af.

## 5. Het verloop volgen

- **De kaart van de automatisering** (tabblad Automatisering). Op de
  statusregel zie je waar de regel is: eerst de wachtstap, daarna
  bijvoorbeeld *Regelt: Woonkamer (sim): temperatuur 18,8 °C, Airco (sim)
  20,5 °C, laatste stap 21:29 omhoog, volgende controle 21:30*. Daaronder
  staat het resultaat van de laatste actie.
- **Klimaat Plugins**: de actuele waarden van tado en de airco. Met
  *Historie* zie je de grafiek. Die beslaat een hele dag, dus een test van
  een half uur is daar alleen een smal stukje aan de rechterkant.
- **Het log van de simulator**, `data/kamersim/hwsim.log`. Elke 15
  seconden staat er een regel zoals:

  ```
  ROOM     kamer 19.20 °C  kamertijd +8:52  cv doel 18.0  airco verwarmen 20.5 (verwarmt)
  ```

  Daarnaast staat er elke instelling in die tado (`tado     overlay 19.0 °C
  …`, `tado     back to the schedule`) en de airco (`MHI      state written
  … aan, verwarmen true, 20.5 °C`) krijgen. Meelezen in een tweede Git
  Bash-venster kan zo:

  ```bash
  tail -f data/kamersim/hwsim.log | grep -v "TLS handshake"
  ```

- **Het log van de gateway** (het Git Bash-venster van stap 1) meldt
  `regel gaat af`, `regel: wachten klaar`, `plugin bediend`, `regel:
  regelen` bij elke stap (meetwaarde, oude en nieuwe stand) en `regel:
  regelen gestopt` met de reden.
- **De regelkaart** op *Automatisering* heeft *Grafiek van de laatste 24
  uur*: tado's temperatuur, de stand van de airco (alleen terwijl deze
  automatisering regelde, gekleurd) en het doel ± marge.

## 6. Wat je zou moeten zien

Met de standaardopties en `-speed 30 -room-start 17`:

| Moment | Wat er gebeurt |
|---|---|
| bij het opslaan | de kamer is 18,0 °C (het schema) → tado gaat op 19 °C |
| na ±15 seconden | tado meet 19 °C → wachtstap klaar → tado terug naar schema, airco aan op verwarmen, 21 °C |
| ±1 minuut later | tado meet 19,4 °C (boven 19,2) → airco een stap omlaag naar 20,5 °C |
| daarna | de kamer blijft op 19,2 °C (binnen de marge), de airco blijft op 20,5 °C |
| eind tijdvenster | *Regelen gestopt: het tijdvenster is voorbij*, de airco gaat uit |

## 7. Zelf verstoren

- **Afstandsbediening nadoen**: zet op *Klimaat Plugins* de temperatuur van
  de airco met **−** op 20 °C. De kamer zakt dan naar 18,7 °C, en binnen
  een minuut zet het regelen de airco weer een stap omhoog naar 20,5 °C.
  Het regelen gaat dus verder vanaf de stand die je zelf instelde.
- **Airco uitzetten**: zet de airco op *Klimaat Plugins* uit. Het regelen
  stopt, met als reden dat het apparaat uit staat.
- **Ander weer of een andere airco**: start opnieuw met bijvoorbeeld
  `-outdoor -5`, dan koelt de kamer sneller af. Met een andere
  `-ac-offset` verandert de temperatuur waarop de airco de kamer houdt.
- **Doel en marge**: wijzig ze op de kaart met − en +. Dat herstart het
  regelen niet; het bereik van de airco schuift mee met het doel.
- **Aanpassen terwijl hij loopt**: open de automatisering tijdens de
  wachtstap of het regelen, verander bijvoorbeeld het tijdvenster of de
  wachttijd en sla op. Hij gaat niet opnieuw af (tado gaat niet nog eens op
  19 °C); de wachtstap of het regelen loopt door met de nieuwe instellingen.
- **Snel opwarmen**: neem het voorbeeld *Airco: snel opwarmen, dan op
  temperatuur houden* (zonder tado-stappen; als meetwaarde de kamer van
  tado of van de airco), met een tijdvenster tot een half uur vanaf nu en
  regelen elke 1 minuut. Start de simulator met `-room-start 16`. De airco
  gaat meteen op 21 °C, doel + 2 (in het log `dir=opwarmen`), en zodra de kamer
  18,8 °C is terug naar 20 °C (`dir=terug`). Met de standaard `-ac-offset`
  van 1,3 blijft de kamer dan op 18,7 °C, net onder de marge, dus het
  regelen zet hem een minuut later naar 20,5 °C. Met een afwijking van +1,5
  in plaats van +1 komt hij direct goed uit.

## 8. Daarna met je echte apparaten

Neem in je echte automatisering de gewone tijden: de wachtstap hooguit 30
minuten en regelen elk kwartier. Bij tado is dat ook nodig vanwege het
daglimiet van de tado-API. Start de gateway dan zonder `HWLOCAL_TADO_URL`,
anders praat de tado-plugin met de simulator in plaats van met tado.
