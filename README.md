# Canzoniere Online — API Reference

API HTTP/JSON per cercare canzoni, generare "canzonieri" (raccolte di canzoni impaginate in PDF via LaTeX) e recuperarne l'esito. Pensata per essere consumata da un'interfaccia web.

**Base URL (ambiente di sviluppo):** `https://api.dev.canzoniereonline.it`

Tutti gli esempi sotto usano questo host. In produzione sarà sufficiente cambiare l'host, i path restano identici.

## Indice

1. [Convenzioni generali](#convenzioni-generali)
2. [Modello dati: Canzone](#modello-dati-canzone)
3. [API di catalogo (ricerca/consultazione canzoni)](#api-di-catalogo-ricercaconsultazione-canzoni)
4. [API di generazione canzoniere](#api-di-generazione-canzoniere)
5. [Oggetto di stato del job (JobStatus)](#oggetto-di-stato-del-job-jobstatus)
6. [Accesso ai file generati](#accesso-ai-file-generati)
7. [Health check](#health-check)
8. [Flussi completi di esempio](#flussi-completi-di-esempio)
9. [Note importanti per chi implementa la UI](#note-importanti-per-chi-implementa-la-ui)

---

## Convenzioni generali

- **Formato**: tutte le richieste/risposte sono JSON (`Content-Type: application/json`), salvo i download binari (PDF/ZIP) esplicitamente indicati.
- **CORS**: abilitato per qualunque origine (`Access-Control-Allow-Origin: *`), metodi `GET, POST, OPTIONS`, header `Content-Type`. Le richieste preflight `OPTIONS` ricevono sempre `204 No Content`. Nessuna configurazione aggiuntiva è necessaria lato browser.
- **Autenticazione**: **nessuna** a livello applicativo. Tutti gli endpoint descritti qui sono pubblici così come implementati. Se serve un livello di autenticazione/autorizzazione va aggiunto a monte (reverse proxy, gateway) — non è qualcosa che la UI deve gestire verso questo backend.
- **Errori**: quando una richiesta fallisce, il corpo della risposta è sempre nella forma:
  ```json
  { "error": "descrizione leggibile dell'errore" }
  ```
  con uno status code HTTP coerente (`400`, `404`, `405`, `500`, `502`, vedi il dettaglio di ogni endpoint).
- **Nessuna paginazione**: gli endpoint di ricerca/elenco (`/search`, `/songs/autore/...`, `/songs/famiglia/...`, `/songs/momento/...`) restituiscono **sempre tutti** i risultati corrispondenti, senza `limit`/`offset`/`page`. Se il catalogo è grande, tienine conto lato UI (es. filtraggio/virtualizzazione client-side).

---

## Modello dati: Canzone

Ci sono due rappresentazioni JSON di una canzone, a seconda dell'endpoint:

### `Song` (oggetto completo — solo `GET /songs/:id`)

```json
{
  "identificatore": "114",
  "titolo": "Laudato sii, mio Signore",
  "autore": "Guerrini/Turoldo",
  "album": "Canti per la liturgia",
  "tonalita": "Re",
  "famiglia": "liturgico",
  "gruppo": "Lodi",
  "momenti": "Ingresso;Pace",
  "data_revisione": "2021-03-10",
  "trascrittore": "M. Rossi",
  "testo_completo": "\\beginsong{Laudato sii, mio Signore}[...]\n...\n\\endsong",
  "testo_semplice": "Laudato sii mio Signore\nper i fratelli e le sorelle...",
  "pdf_testo_url": "https://.../canzoni/114/114_lyric.pdf",
  "pdf_accordi_url": "https://.../canzoni/114/114.pdf"
}
```

### `SongSummary` (versione leggera — endpoint di elenco/ricerca)

Stessi campi tranne `gruppo`, `data_revisione`, `trascrittore`, `testo_completo`, `testo_semplice` (niente testo completo, per tenere le risposte leggere quando si elencano molti risultati).

```json
{
  "identificatore": "114",
  "titolo": "Laudato sii, mio Signore",
  "autore": "Guerrini/Turoldo",
  "album": "Canti per la liturgia",
  "tonalita": "Re",
  "famiglia": "liturgico",
  "momenti": "Ingresso;Pace",
  "pdf_testo_url": "https://.../canzoni/114/114_lyric.pdf",
  "pdf_accordi_url": "https://.../canzoni/114/114.pdf"
}
```

**Note sui campi:**

| Campo | Tipo | Note |
|---|---|---|
| `identificatore` | string | Identificativo univoco della canzone. È il valore da usare in `canzoni[].identificatore` quando si crea un canzoniere (vedi sotto). |
| `momenti` | string | Lista di tag separati da **punto e virgola** (es. `"Ingresso;Pace;Comunione"`), non un array JSON. |
| `famiglia` | string | Categoria/famiglia liturgica o tematica. Il match su questo campo è sempre case-insensitive lato server. |
| `testo_completo` | string | Sorgente LaTeX grezzo del blocco `\beginsong...\endsong`. Presente solo su `GET /songs/:id`. |
| `pdf_testo_url` / `pdf_accordi_url` | string | URL diretti ai PDF della singola canzone (solo testo / con accordi), generati e mantenuti in cache lato server. **Presenti solo se S3 è abilitato lato backend**; altrimenti questi campi sono assenti (omessi, non stringa vuota). Sono URL "best effort": puntano a dove il PDF *dovrebbe* trovarsi, senza garanzia che sia già stato generato in quel momento (la generazione è un processo asincrono in background lato server, indipendente dalle richieste API). |

---

## API di catalogo (ricerca/consultazione canzoni)

Endpoint pensati per popolare l'interfaccia di selezione delle canzoni prima di creare un canzoniere.

### `GET /songs/:id`

Recupera una singola canzone per identificatore, con testo completo.

**Esempio:** `GET /songs/114`

**200 OK** → oggetto `Song` (vedi sopra).
**404 Not Found** → `{"error": "canzone non trovata"}`
**400 Bad Request** → se l'id è vuoto (es. `GET /songs/`).

---

### `GET /autori`

Elenco di tutti gli autori distinti presenti nel database, ordinati alfabeticamente (case-insensitive).

**200 OK:**
```json
["Anonimo", "De Andrè, Fabrizio", "Guerrini/Turoldo", "..."]
```

### `GET /famiglie`

Elenco delle famiglie/categorie distinte, **sempre restituite in minuscolo**.

**200 OK:**
```json
["giovani", "liturgico", "natalizio"]
```

### `GET /momenti`

Elenco dei singoli "momenti" (tag), già decomposti dal formato `;`-separated e deduplicati.

**200 OK:**
```json
["Ingresso", "Pace", "Comunione", "Offertorio"]
```

Questi tre endpoint sono pensati per popolare filtri a tendina/checkbox nella UI.

---

### `GET /songs/autore/:autore`

Canzoni il cui autore **contiene** la stringa indicata (match parziale, case-insensitive).

**Esempio:** `GET /songs/autore/turoldo`

### `GET /songs/famiglia/:famiglia`

Canzoni la cui famiglia è **esattamente uguale** (case-insensitive, ma non parziale) al valore indicato.

**Esempio:** `GET /songs/famiglia/liturgico`

### `GET /songs/momento/:momento`

Canzoni che hanno il valore indicato come uno dei tag nel campo `momenti` (match esatto sul singolo tag, **case-sensitive**; non un "contiene" sulla stringa intera).

**Esempio:** `GET /songs/momento/Ingresso`

Tutti e tre restituiscono lo stesso formato:

**200 OK:**
```json
{
  "count": 2,
  "results": [
    { "identificatore": "114", "titolo": "...", "...": "..." },
    { "identificatore": "205", "titolo": "...", "...": "..." }
  ]
}
```

**400 Bad Request** se il parametro nel path è vuoto.

---

### `GET /search`

Ricerca combinata con più filtri opzionali passati come **query string**. Tutti i filtri forniti vengono combinati in **AND** (non OR). Se non si passa nessun filtro, restituisce l'intero catalogo.

| Parametro | Match | Descrizione |
|---|---|---|
| `q` | LIKE parziale, case-insensitive | Cerca in `titolo`, `autore`, `album`, `testo_semplice` (match se compare in **almeno uno** di questi campi) |
| `titolo` | LIKE parziale | Filtra sul titolo |
| `autore` | LIKE parziale | Filtra sull'autore |
| `album` | LIKE parziale | Filtra sull'album |
| `tonalita` | LIKE parziale | Filtra sulla tonalità |
| `gruppo` | LIKE parziale | Filtra sul gruppo |
| `famiglia` | uguaglianza case-insensitive (con LIKE, quindi in pratica parziale anch'esso) | Filtra sulla famiglia |
| `momento` | match esatto sul singolo tag (case-sensitive) | Stesso comportamento di `/songs/momento/:momento` |
| `testo` | LIKE parziale | Cerca sia in `testo_semplice` che in `testo_completo` |

**Esempio:** `GET /search?q=laudato&famiglia=liturgico&momento=Ingresso`

**200 OK:**
```json
{ "count": 1, "results": [ { "identificatore": "114", "...": "..." } ] }
```

---

## API di generazione canzoniere

Qui si passa dalla scelta delle canzoni alla generazione vera e propria del PDF/canzoniere. Il processo è **sempre asincrono**: la richiesta POST restituisce subito un `job_id`, la generazione (compilazione LaTeX) avviene in background e va monitorata tramite l'endpoint di stato.

Ci sono **due varianti** dell'endpoint di creazione, che si differenziano per come viene consegnato il risultato finale:

| Endpoint | Consegna risultato | Richiede `email` |
|---|---|---|
| `POST /canzoniere` | Invio ZIP via **email** (+ upload su storage se abilitato) | **Sì**, obbligatorio |
| `POST /canzoniere/download` | Solo upload su storage; il file va scaricato tramite gli URL S3 (vedi sezione successiva) | No |

⚠️ **Importante:** `POST /canzoniere/download` ha senso solo se lo storage S3 è abilitato lato server. Se S3 non è abilitato, questo endpoint accetta comunque la richiesta (`202`) ma il risultato non sarà recuperabile in nessun modo (nessuna email, nessun file da scaricare) — per una UI generica conviene sempre usare `POST /canzoniere` quando serve garantire che l'utente riceva il file, oppure verificare a monte che lo storage sia attivo prima di offrire il flusso "download".

### Corpo della richiesta — oggetto `CanzonieRequest`

Identico per entrambi gli endpoint:

```json
{
  "titoletto": "Parrocchia di San Marco",
  "titolo": "Canzoniere Estate 2026",
  "sottotitolo": "Edizione campo giovani",
  "num_colonne": 1,
  "formato": "standard",
  "accordi": "both",
  "font": "libertine",
  "minorsign": "m",
  "indici": ["titleindex", "authorsindex"],
  "copertina": "cover",
  "email": "utente@esempio.it",
  "canzoni": [
    { "identificatore": "114", "semitoni": 0 },
    { "identificatore": "205", "semitoni": -2 }
  ]
}
```

#### Riferimento campi

| Campo | Tipo | Obbligatorio | Default se omesso | Descrizione |
|---|---|---|---|---|
| `titoletto` | string | no | `""` | Riga piccola sopra il titolo in copertina |
| `titolo` | string | no | `""` (diventa `"Canzoniere"` solo nell'oggetto della email) | Titolo principale in copertina |
| `sottotitolo` | string | no | `""` | Sottotitolo in copertina |
| `num_colonne` | int | no | `1` (qualunque valore `<= 0` viene forzato a `1`) | Numero di colonne di impaginazione del testo |
| `formato` | string | no | `"standard"` | Opzione di formato passata alla classe LaTeX. **Non validata dal backend**: qualunque stringa passa direttamente come opzione `\documentclass`; i valori validi sono definiti dal template LaTeX, non dal codice Go. Verificare col template quali formati esistono oltre a `standard`. |
| `accordi` | string | no | vedi nota ⚠️ sotto | Controlla quali versioni del PDF vengono generate: `"lyric"` (solo testo), `"chorded"` (con accordi), `"both"` (entrambe) |
| `font` | string | no | `"libertine"` | Font passato come opzione `\documentclass`. Non validato dal backend, dipende dal template. |
| `minorsign` | string | no | *(omesso)* | Se non vuoto, viene aggiunta l'opzione `minorsign=<valore>` (tipicamente `"m"` o `"-"` per la notazione degli accordi minori — dipende dal template). Se vuoto, l'opzione non viene passata affatto (non è un default implicito, è proprio assente). |
| `indici` | array di string | no | `[]` | Indici da includere. Valori riconosciuti e processati automaticamente (generazione pagine indice via `xindy`): `"titleindex"` (indice per titolo), `"authorsindex"` (indice per autore), `"tematicindex"` (indice tematico — se presente, il backend inietta automaticamente il tag `\momenti{...}` di ogni canzone nel sorgente, non serve altro). ⚠️ Qualunque altro valore in questo array viene comunque passato come opzione `\documentclass` grezza, senza validazione: se non è un'opzione riconosciuta dal template LaTeX la compilazione può fallire. |
| `copertina` | string | no | `"cover"` | Opzione copertina passata al template. Non validata dal backend. |
| `email` | string | **sì, solo per `POST /canzoniere`** | — | Indirizzo a cui inviare il PDF finale via email. Ignorato (può essere omesso) su `POST /canzoniere/download`. |
| `canzoni` | array di oggetti | **sì (almeno 1 elemento)** | — | Elenco delle canzoni da includere, in ordine. Vedi sotto. |

`canzoni[]` — ogni elemento:

| Campo | Tipo | Obbligatorio | Descrizione |
|---|---|---|---|
| `identificatore` | string | sì | Deve corrispondere a un `identificatore` esistente nel catalogo (vedi API sopra). Se non trovato, il job termina in errore (`"canzone non trovata: <id>"`). |
| `semitoni` | int | no (default `0`) | Trasposizione della canzone in semitoni: positivo per alzare il tono, negativo per abbassarlo, `0` per nessuna trasposizione. Il backend non impone un range: eventuali limiti sono quelli supportati dal comando `\transpose` del template LaTeX. |

> ⚠️ **Nota importante su `accordi`:** se il campo viene omesso o lasciato vuoto (`""`), **non viene generato nessun PDF** (né la versione "solo testo" né quella "con accordi") — il job comunque va a buon fine (status `done`), ma lo ZIP/upload conterrà solo i sorgenti LaTeX e nessun PDF. Questo è un comportamento del backend attuale, non un'opzione "usa il default sensato": **la UI dovrebbe sempre inviare esplicitamente uno tra `"lyric"`, `"chorded"`, `"both"`** e non fare affidamento su un default implicito.

### `POST /canzoniere`

**Richiede** `Content-Type: application/json` e corpo come sopra, con `email` obbligatoria.

**Risposta immediata — `202 Accepted`:**
```json
{
  "status": "accepted",
  "job_id": "202607021530a1b2c3d4e5f6...64charhex",
  "message": "Generazione avviata — riceverai lo ZIP a utente@esempio.it",
  "status_url": "https://<bucket-endpoint>/<bucket>/canzoniere/2026/07/02/202607021530.../status.json"
}
```
Il campo `status_url` è presente **solo se lo storage S3 è abilitato** lato backend; in caso contrario è assente dalla risposta. Anche quando presente, per la UI è preferibile interrogare `GET /canzoniere/status/:job_id` (vedi sotto) invece di leggere direttamente da `status_url`, perché funziona indipendentemente dal fatto che il bucket sia pubblico o privato.

**Errori:**
- `405` se il metodo non è `POST`
- `400` `{"error": "JSON non valido: ..."}` se il body non è JSON valido
- `400` `{"error": "nessuna canzone specificata"}` se `canzoni` è vuoto/assente
- `400` `{"error": "email destinatario mancante"}` se `email` è vuota/assente

Dopodiché il job prosegue **in background**: la generazione, l'eventuale upload su storage e l'invio email avvengono dopo che la risposta HTTP è già stata restituita. L'unico modo per sapere come è andata è interrogare lo stato (vedi sezione successiva) oppure attendere l'email (che arriva sia in caso di successo, con lo ZIP allegato, sia in caso di errore, con il messaggio di errore nel corpo).

### `POST /canzoniere/download`

Stessa richiesta, stessa validazione, **eccetto** che `email` non è richiesta.

**Risposta immediata — `202 Accepted`:**
```json
{
  "status": "accepted",
  "job_id": "202607021530a1b2c3d4e5f6...64charhex",
  "message": "Generazione avviata",
  "status_url": "https://.../status.json"
}
```
(`status_url` presente solo se S3 abilitato, come sopra.)

**Errori:** stessi di `/canzoniere` tranne il controllo sull'email (che qui non esiste).

---

## Oggetto di stato del job (`JobStatus`)

### `GET /canzoniere/status/:job_id`

Restituisce lo stato corrente del job identificato da `job_id` (quello ricevuto nella risposta `202` di uno dei due endpoint precedenti).

⚠️ **Disponibile solo se lo storage S3 è abilitato lato backend.** Se S3 non è abilitato, questo endpoint restituisce sempre `404` con `{"error": "S3 non abilitato: stato non disponibile"}`, qualunque sia il `job_id`.

**Esempio:** `GET /canzoniere/status/202607021530a1b2c3d4e5f6...`

**200 OK** → oggetto `JobStatus`:

```json
{
  "job_id": "202607021530a1b2c3d4e5f6...64charhex",
  "status": "compiling",
  "message": "Compilazione con lualatex in corso",
  "created_at": "2026-07-02T15:30:04Z",
  "updated_at": "2026-07-02T15:30:11Z"
}
```

| Campo | Tipo | Note |
|---|---|---|
| `job_id` | string | Uguale a quello richiesto |
| `status` | string | Uno dei valori descritti nella tabella sotto |
| `message` | string | Descrizione leggibile dello step corrente, in italiano — pensata per essere mostrata direttamente all'utente |
| `error` | string | **Presente solo se `status == "error"`.** Messaggio tecnico dell'errore (può contenere dettagli LaTeX/DB, non necessariamente adatto a essere mostrato as-is all'utente finale) |
| `created_at` | string (RFC3339, UTC) | Timestamp di creazione del job. **Non cambia mai** durante tutto il ciclo di vita del job: utile per calcolare il tempo trascorso |
| `updated_at` | string (RFC3339, UTC) | Timestamp dell'ultimo aggiornamento di stato. Cambia a ogni transizione |
| `files` | array di oggetti | **Presente solo quando `status == "done"`.** Vedi struttura sotto |

### Macchina a stati di `status`

```
queued → fetching_songs → compiling → packaging → uploading → done
   ↓            ↓             ↓            ↓            ↓
   └────────────┴─────────────┴────────────┴──────► error (in qualsiasi step)
```

| `status` | `message` tipico | Significato |
|---|---|---|
| `queued` | `"In coda"` | Job accettato, in attesa che si liberi lo slot di compilazione (il server compila **una sola** richiesta LaTeX per volta, per limitare l'uso di RAM) |
| `fetching_songs` | `"Recupero N canzoni dal database"` | Sta leggendo i testi delle canzoni richieste dal database |
| `compiling` | `"Compilazione con lualatex in corso"` | Compilazione LaTeX in corso (motore configurabile lato server, tipicamente `lualatex`) — è lo step più lento |
| `packaging` | `"Creazione archivio ZIP"` | Sta impacchettando tutti i file prodotti (PDF, sorgenti `.tex`, file ausiliari) in uno ZIP |
| `uploading` | `"Caricamento file sullo Space"` | Sta caricando i file sullo storage S3 |
| `done` | `"Canzoniere pronto"` | Completato. Il campo `files` elenca tutti i file disponibili |
| `error` | `"Generazione fallita"` | Fallito in uno qualunque degli step precedenti. Vedi campo `error` per il dettaglio |

Gli step `uploading` e `done`-con-`files` vengono raggiunti solo se lo storage S3 è abilitato lato server (è comunque il caso normale, dato che senza S3 l'endpoint di stato non è nemmeno raggiungibile, come detto sopra).

**Esempio di stato finale (`done`):**
```json
{
  "job_id": "202607021530a1b2c3d4e5f6...64charhex",
  "status": "done",
  "message": "Canzoniere pronto",
  "created_at": "2026-07-02T15:30:04Z",
  "updated_at": "2026-07-02T15:31:47Z",
  "files": [
    {
      "name": "canzoniere_20260702_153147.zip",
      "key": "canzoniere/2026/07/02/202607021530.../canzoniere_20260702_153147.zip",
      "url": "https://<endpoint-privato>/<bucket>/canzoniere/2026/07/02/.../canzoniere_....zip",
      "public_url": "https://<endpoint-pubblico>/<bucket>/canzoniere/2026/07/02/.../canzoniere_....zip"
    },
    {
      "name": "canzoniereonline.pdf",
      "key": "canzoniere/2026/07/02/202607021530.../canzoniereonline.pdf",
      "url": "https://.../canzoniereonline.pdf",
      "public_url": "https://.../canzoniereonline.pdf"
    }
  ]
}
```

`files[]` contiene **tutti** i file prodotti dalla compilazione: non solo lo ZIP finale e i PDF, ma anche i sorgenti `.tex`, i file ausiliari LaTeX (`.aux`, `.toc`, `.log`, ecc.) e un `payload.json` con la richiesta originale. Per una UI orientata all'utente finale, tipicamente interessano solo lo ZIP e/o i PDF (riconoscibili dall'estensione in `name`); gli altri file sono più utili per debug.

Ogni elemento di `files[]`:

| Campo | Descrizione |
|---|---|
| `name` | Nome del file (es. `canzoniereonline.pdf`) |
| `key` | Percorso completo nel bucket S3 (utile solo per debug/riferimento interno) |
| `url` | URL "grezzo" verso l'endpoint privato dello storage — **richiede firma per essere effettivamente scaricabile**, non usarlo direttamente da browser |
| `public_url` | URL verso l'endpoint pubblico dello storage — funziona solo se il bucket è configurato come pubblicamente leggibile |

**Consiglio per la UI:** non usare `url`/`public_url` per i download. Usa invece gli endpoint proxy dell'applicazione descritti sotto (`/s3/:job_id/:filename` e `/s3/:job_id/download`), che funzionano sempre indipendentemente dalla configurazione del bucket, perché firmano la richiesta lato server.

**Errori:**
- `400` `{"error": "job_id mancante"}` se il segmento di path è vuoto
- `404` `{"error": "job non trovato"}` se non esiste (job_id sbagliato, oppure — vedi nota sotto — interrogato troppo presto)
- `502` `{"error": "errore lettura stato: ..."}` in caso di problemi di rete/autenticazione verso lo storage

---

## Accesso ai file generati

Due endpoint per scaricare i file di un job **senza dover gestire credenziali S3 lato client**: la firma viene generata server-side a ogni richiesta.

### `GET /s3/:job_id/:filename`

Redirect (`302`) verso un URL firmato temporaneo (presigned) del singolo file richiesto. Il browser segue automaticamente il redirect e scarica/visualizza il file.

**Esempio:** `GET /s3/202607021530.../canzoniereonline.pdf` → `302 Found` con header `Location` verso l'URL firmato (valido per un tempo limitato, tipicamente 1 ora, configurato lato server).

**Errori:** `400` se il path non è nella forma `/s3/<job_id>/<filename>`; `404` se S3 non è abilitato; `500` se la firma non può essere generata.

### `GET /s3/:job_id/download`

Scarica **tutti** i file del job aggregati in un unico file ZIP, generato al volo dal server (non richiede che esista già uno ZIP pre-costruito).

**Esempio:** `GET /s3/202607021530.../download`

**200 OK**, `Content-Type: application/zip`, `Content-Disposition: attachment; filename="canzoniere_202607021530.zip"`.

**Errori:** `400` se manca il `job_id`; `404` se S3 non è abilitato o non viene trovato nessun file per quel job; `502` in caso di errore nel listare i file.

---

## Health check

### `GET /`

**200 OK:**
```json
{ "service": "canzoniere-api", "status": "ok" }
```

Qualunque altro path non riconosciuto dal router restituisce `404` con `{"error": "endpoint non trovato"}`.

---

## Flussi completi di esempio

### Flusso A — invio via email

```
1. L'utente cerca/seleziona canzoni
   GET /search?q=laudato
   GET /songs/famiglia/liturgico
   ...

2. L'utente compone il canzoniere e conferma
   POST /canzoniere
   {
     "titolo": "Canzoniere Estate 2026",
     "accordi": "both",
     "email": "utente@esempio.it",
     "canzoni": [
       { "identificatore": "114", "semitoni": 0 },
       { "identificatore": "205", "semitoni": -2 }
     ]
   }
   → 202 { "job_id": "...", "status_url": "..." }

3. (opzionale) La UI mostra un indicatore di avanzamento, facendo polling:
   GET /canzoniere/status/<job_id>   ogni 2-3 secondi
   → segue status: queued → fetching_songs → compiling → packaging
     → uploading → done

4. Quando status == "done" la UI può mostrare "controlla la tua email"
   (l'email con lo ZIP allegato arriva comunque in modo indipendente
   dal polling, anche se la UI viene chiusa prima che status diventi "done")
```

### Flusso B — download diretto (richiede S3 abilitato lato server)

```
1-2. Come sopra, ma:
   POST /canzoniere/download
   { "accordi": "both", "canzoni": [ ... ] }   // niente "email"
   → 202 { "job_id": "...", "status_url": "..." }

3. Polling status come sopra, fino a status == "done"

4. Offri il download:
   - singolo file:  GET /s3/<job_id>/canzoniereonline.pdf
   - tutto insieme: GET /s3/<job_id>/download
   (oppure usa direttamente gli URL in files[] dello stato "done",
    preferendo comunque gli endpoint /s3/... come spiegato sopra)
```

---

## Note importanti per chi implementa la UI

- **Nessuna autenticazione applicativa**: non serve gestire login/token verso questa API così come descritta.
- **`accordi` va sempre specificato esplicitamente** (`"lyric"`, `"chorded"` o `"both"`): lasciarlo vuoto produce un job "riuscito" ma senza nessun PDF dentro.
- **Formato, font e copertina non sono validati dal backend**: sono opzioni passate al template LaTeX così come arrivano. Se la UI deve offrire dei selettori con valori garantiti, conviene concordare/verificare l'elenco esatto con chi gestisce il template LaTeX, piuttosto che assumerlo da questa documentazione.
- **`indici` accetta liberamente qualsiasi stringa**, ma solo `titleindex`, `authorsindex`, `tematicindex` sono garantiti funzionare correttamente; altri valori rischiano di rompere la compilazione.
- **Polling dello stato**: subito dopo aver ricevuto il `202` da `POST /canzoniere` o `POST /canzoniere/download`, è possibile (raramente) ricevere un `404` dal primo tentativo di `GET /canzoniere/status/:job_id`, per una breve finestra di race condition tra la risposta e la scrittura del primissimo stato. Consigliato un piccolo retry (es. ritenta dopo 500ms-1s) prima di considerare il job_id come effettivamente inesistente.
- **Nessuna paginazione** su `/search` e affini: se il catalogo cresce molto, valutare filtri lato UI o chiedere di introdurre `limit`/`offset` lato backend.
- **`GET /canzoniere/status/:job_id` e gli endpoint `/s3/...` funzionano solo se lo storage S3 è abilitato lato server.** Se non lo è, l'unico modo per ottenere il risultato è `POST /canzoniere` (via email); `POST /canzoniere/download` in quel caso non è utilizzabile.
- I `job_id` sono stringhe di 76 caratteri (12 cifre di timestamp UTC + 64 caratteri esadecimali) — trattali come stringhe opache, non serve/va fatto parsing lato client.