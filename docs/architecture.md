<div>
<style scoped>
  * {
    margin: 0;
    padding: 0;
    box-sizing: border-box;
  }
  body {
    font-family: 'JetBrains Mono', monospace;
    background: #020617;
    min-height: 100vh;
    padding: 2rem;
    color: white;
  }
  .container {
    max-width: 1240px;
    margin: 0 auto;
  }
  .header {
    margin-bottom: 2rem;
  }
  .header-row {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin-bottom: 0.5rem;
  }
  .pulse-dot {
    width: 12px;
    height: 12px;
    background: #22d3ee;
    border-radius: 50%;
    animation: pulse 2s infinite;
  }
  @keyframes pulse {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.5;
    }
  }
  h1 {
    font-size: 1.5rem;
    font-weight: 700;
    letter-spacing: -0.025em;
  }
  .subtitle {
    color: #94a3b8;
    font-size: 0.875rem;
    margin-left: 1.75rem;
  }
  .diagram-container {
    background: rgba(15, 23, 42, 0.5);
    border-radius: 1rem;
    border: 1px solid #1e293b;
    padding: 1.5rem;
    overflow-x: auto;
  }
  svg {
    width: 100%;
    min-width: 900px;
    display: block;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 1rem;
    margin-top: 2rem;
  }
  .card {
    background: rgba(15, 23, 42, 0.5);
    border-radius: 0.75rem;
    border: 1px solid #1e293b;
    padding: 1.25rem;
  }
  .card-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.75rem;
  }
  .card-dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
  }
  .card-dot.cyan {
    background: #22d3ee;
  }
  .card-dot.emerald {
    background: #34d399;
  }
  .card-dot.violet {
    background: #a78bfa;
  }
  .card-dot.amber {
    background: #fbbf24;
  }
  .card-dot.rose {
    background: #fb7185;
  }
  .card h3 {
    font-size: 0.875rem;
    font-weight: 600;
  }
  .card ul {
    list-style: none;
    color: #94a3b8;
    font-size: 0.75rem;
  }
  .card li {
    margin-bottom: 0.375rem;
  }
  .footer {
    text-align: center;
    margin-top: 1.5rem;
    color: #475569;
    font-size: 0.75rem;
  }
</style>
<div class="container">
  <!-- Header -->
  <div class="header">
    <div class="header-row">
      <div class="pulse-dot"></div>
      <h1>Arbiter Architecture</h1>
    </div>
    <p class="subtitle">Single-user LLM proxy &#8212; request lifecycle, classification, routing, upstream relay, event
      store, admin UI</p>
  </div>
  <!-- Main Diagram -->
  <div class="diagram-container">
    <svg viewBox="0 0 1180 970">
      <!-- Definitions -->
      <defs>
        <marker id="arrowhead" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto">
          <polygon points="0 0, 10 3.5, 0 7" fill="#64748b" />
        </marker>
        <marker id="arrowhead-emerald" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto">
          <polygon points="0 0, 10 3.5, 0 7" fill="#34d399" />
        </marker>
        <marker id="arrowhead-rose" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto">
          <polygon points="0 0, 10 3.5, 0 7" fill="#fb7185" />
        </marker>
        <marker id="arrowhead-cyan" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto">
          <polygon points="0 0, 10 3.5, 0 7" fill="#22d3ee" />
        </marker>
        <pattern id="grid" width="40" height="40" patternUnits="userSpaceOnUse">
          <path d="M 40 0 L 0 0 0 40" fill="none" stroke="#1e293b" stroke-width="0.5" />
        </pattern>
      </defs>
      <!-- Background Grid -->
      <rect width="100%" height="100%" fill="url(#grid)" />
      <!-- ============================ ARROWS (drawn early, behind boxes) ============================ -->
      <!-- clients -> ingress -->
      <line x1="170" y1="140" x2="211" y2="133" stroke="#94a3b8" stroke-width="1.5" marker-end="url(#arrowhead)" />
      <line x1="170" y1="216" x2="211" y2="153" stroke="#94a3b8" stroke-width="1.5" marker-end="url(#arrowhead)" />
      <line x1="170" y1="292" x2="211" y2="205" stroke="#94a3b8" stroke-width="1.5" marker-end="url(#arrowhead)" />
      <!-- ingress -> pipeline normalize -->
      <line x1="400" y1="139" x2="451" y2="132" stroke="#34d399" stroke-width="1.5"
        marker-end="url(#arrowhead-emerald)" />
      <!-- pipeline stage to stage -->
      <line x1="565" y1="164" x2="565" y2="184" stroke="#34d399" stroke-width="1.5"
        marker-end="url(#arrowhead-emerald)" />
      <line x1="565" y1="250" x2="565" y2="270" stroke="#34d399" stroke-width="1.5"
        marker-end="url(#arrowhead-emerald)" />
      <line x1="565" y1="336" x2="565" y2="356" stroke="#34d399" stroke-width="1.5"
        marker-end="url(#arrowhead-emerald)" />
      <line x1="565" y1="422" x2="565" y2="442" stroke="#34d399" stroke-width="1.5"
        marker-end="url(#arrowhead-emerald)" />
      <line x1="565" y1="508" x2="565" y2="528" stroke="#34d399" stroke-width="1.5"
        marker-end="url(#arrowhead-emerald)" />
      <!-- upstream relay -> providers -->
      <path d="M 675 562 L 722 562 L 722 308 L 736 308" fill="none" stroke="#34d399" stroke-width="1.5"
        marker-end="url(#arrowhead-emerald)" />
      <text x="716" y="430" fill="#94a3b8" font-size="8" text-anchor="middle" transform="rotate(-90 716 430)">HTTPS
        &#183; SSE</text>
      <!-- classifier model call (out of band, dashed) -->
      <line x1="675" y1="390" x2="736" y2="150" stroke="#fb7185" stroke-width="1.5" stroke-dasharray="5,5"
        marker-end="url(#arrowhead-rose)" />
      <text x="698" y="268" fill="#fb7185" font-size="7">llm / decisions model call</text>
      <!-- pipeline -> store -->
      <line x1="565" y1="620" x2="565" y2="648" stroke="#a78bfa" stroke-width="1.5" marker-end="url(#arrowhead)" />
      <text x="575" y="640" fill="#94a3b8" font-size="8">1 row per request &#183; every call</text>
      <!-- store -> admin ui -->
      <line x1="690" y1="745" x2="736" y2="745" stroke="#22d3ee" stroke-width="1.5" marker-end="url(#arrowhead-cyan)" />
      <text x="713" y="739" fill="#94a3b8" font-size="7" text-anchor="middle">Reader</text>
      <!-- ============================ CLIENTS ============================ -->
      <text x="30" y="96" fill="#94a3b8" font-size="10" font-weight="600">Clients (external)</text>
      <g>
        <rect x="30" y="112" width="140" height="56" rx="6" fill="#0f172a" />
        <rect x="30" y="112" width="140" height="56" rx="6" fill="rgba(30, 41, 59, 0.5)" stroke="#94a3b8"
          stroke-width="1.5" />
        <text x="100" y="134" fill="white" font-size="11" font-weight="600" text-anchor="middle">Hermes</text>
        <text x="100" y="152" fill="#94a3b8" font-size="9" text-anchor="middle">OpenAI wire</text>
        <rect x="30" y="188" width="140" height="56" rx="6" fill="#0f172a" />
        <rect x="30" y="188" width="140" height="56" rx="6" fill="rgba(30, 41, 59, 0.5)" stroke="#94a3b8"
          stroke-width="1.5" />
        <text x="100" y="210" fill="white" font-size="11" font-weight="600" text-anchor="middle">opencode</text>
        <text x="100" y="228" fill="#94a3b8" font-size="9" text-anchor="middle">OpenAI wire</text>
        <rect x="30" y="264" width="140" height="56" rx="6" fill="#0f172a" />
        <rect x="30" y="264" width="140" height="56" rx="6" fill="rgba(30, 41, 59, 0.5)" stroke="#94a3b8"
          stroke-width="1.5" />
        <text x="100" y="286" fill="white" font-size="11" font-weight="600" text-anchor="middle">Claude Code</text>
        <text x="100" y="304" fill="#94a3b8" font-size="9" text-anchor="middle">Anthropic wire</text>
      </g>
      <!-- ============================ INGRESS BOUNDARY ============================ -->
      <rect x="200" y="80" width="200" height="230" rx="12" fill="rgba(251, 191, 36, 0.05)" stroke="#fbbf24"
        stroke-width="1" stroke-dasharray="8,4" />
      <text x="210" y="98" fill="#fbbf24" font-size="10" font-weight="600">Arbiter &#183; 127.0.0.1:8080</text>
      <g>
        <rect x="215" y="112" width="170" height="54" rx="6" fill="#0f172a" />
        <rect x="215" y="112" width="170" height="54" rx="6" fill="rgba(6, 78, 59, 0.4)" stroke="#34d399"
          stroke-width="1.5" />
        <text x="300" y="132" fill="white" font-size="11" font-weight="600"
          text-anchor="middle">/chat/completions</text>
        <text x="300" y="148" fill="#94a3b8" font-size="9" text-anchor="middle">+ /v1/chat/completions</text>
        <rect x="215" y="176" width="170" height="54" rx="6" fill="#0f172a" />
        <rect x="215" y="176" width="170" height="54" rx="6" fill="rgba(6, 78, 59, 0.4)" stroke="#34d399"
          stroke-width="1.5" />
        <text x="300" y="196" fill="white" font-size="11" font-weight="600" text-anchor="middle">/v1/messages</text>
        <text x="300" y="212" fill="#94a3b8" font-size="9" text-anchor="middle">Anthropic ingress</text>
        <rect x="215" y="240" width="170" height="44" rx="6" fill="#0f172a" />
        <rect x="215" y="240" width="170" height="44" rx="6" fill="rgba(6, 78, 59, 0.4)" stroke="#34d399"
          stroke-width="1.5" />
        <text x="300" y="258" fill="white" font-size="10" font-weight="600" text-anchor="middle">/v1/models &#183;
          /health</text>
        <text x="300" y="273" fill="#94a3b8" font-size="8" text-anchor="middle">answered directly</text>
      </g>
      <!-- ============================ PIPELINE BOUNDARY ============================ -->
      <rect x="440" y="60" width="250" height="560" rx="12" fill="rgba(251, 191, 36, 0.05)" stroke="#fbbf24"
        stroke-width="1" stroke-dasharray="8,4" />
      <text x="452" y="78" fill="#fbbf24" font-size="10" font-weight="600">internal/pipeline</text>
      <g>
        <!-- normalize -->
        <rect x="455" y="100" width="220" height="64" rx="6" fill="#0f172a" />
        <rect x="455" y="100" width="220" height="64" rx="6" fill="rgba(6, 78, 59, 0.4)" stroke="#34d399"
          stroke-width="1.5" />
        <text x="565" y="125" fill="white" font-size="12" font-weight="600" text-anchor="middle">Normalize
          (translator)</text>
        <text x="565" y="145" fill="#94a3b8" font-size="9" text-anchor="middle">Anthropic &#8646; OpenAI &#8646;
          Normalized</text>
        <!-- session key + capture -->
        <rect x="455" y="186" width="220" height="64" rx="6" fill="#0f172a" />
        <rect x="455" y="186" width="220" height="64" rx="6" fill="rgba(6, 78, 59, 0.4)" stroke="#34d399"
          stroke-width="1.5" />
        <text x="565" y="211" fill="white" font-size="12" font-weight="600" text-anchor="middle">Session key +
          capture</text>
        <text x="565" y="231" fill="#94a3b8" font-size="8" text-anchor="middle">hash(system + first user turn) &#183; as
          sent</text>
        <!-- pre-guardrails (security) -->
        <rect x="455" y="272" width="220" height="64" rx="6" fill="#0f172a" />
        <rect x="455" y="272" width="220" height="64" rx="6" fill="rgba(136, 19, 55, 0.4)" stroke="#fb7185"
          stroke-width="1.5" />
        <text x="565" y="297" fill="white" font-size="12" font-weight="600" text-anchor="middle">Pre-guardrails</text>
        <text x="565" y="317" fill="#94a3b8" font-size="8" text-anchor="middle">system_prompt &#183; rate_limit &#183;
          rewrite</text>
        <!-- classify -->
        <rect x="455" y="358" width="220" height="64" rx="6" fill="#0f172a" />
        <rect x="455" y="358" width="220" height="64" rx="6" fill="rgba(6, 78, 59, 0.4)" stroke="#34d399"
          stroke-width="1.5" />
        <text x="565" y="383" fill="white" font-size="12" font-weight="600" text-anchor="middle">Classify (per
          axis)</text>
        <text x="565" y="400" fill="#94a3b8" font-size="8" text-anchor="middle">heuristic &#183; llm &#183;
          decisions</text>
        <text x="565" y="413" fill="#94a3b8" font-size="7" text-anchor="middle">domain &#183; effort &#183; cost_class
          &#183; capabilities</text>
        <!-- route -->
        <rect x="455" y="444" width="220" height="64" rx="6" fill="#0f172a" />
        <rect x="455" y="444" width="220" height="64" rx="6" fill="rgba(6, 78, 59, 0.4)" stroke="#34d399"
          stroke-width="1.5" />
        <text x="565" y="469" fill="white" font-size="12" font-weight="600" text-anchor="middle">Route</text>
        <text x="565" y="489" fill="#94a3b8" font-size="8" text-anchor="middle">alias &#8594; policy rules &#8594;
          simple fallback</text>
        <!-- upstream -->
        <rect x="455" y="530" width="220" height="64" rx="6" fill="#0f172a" />
        <rect x="455" y="530" width="220" height="64" rx="6" fill="rgba(6, 78, 59, 0.4)" stroke="#34d399"
          stroke-width="1.5" />
        <text x="565" y="555" fill="white" font-size="12" font-weight="600" text-anchor="middle">Upstream call +
          relay</text>
        <text x="565" y="575" fill="#94a3b8" font-size="8" text-anchor="middle">SSE relay &#183; cooldown &#8594;
          fallback route</text>
      </g>
      <!-- ============================ PROVIDERS ============================ -->
      <text x="740" y="96" fill="#94a3b8" font-size="10" font-weight="600">providers (arbiter.yaml)</text>
      <g>
        <rect x="740" y="118" width="200" height="56" rx="6" fill="#0f172a" />
        <rect x="740" y="118" width="200" height="56" rx="6" fill="rgba(30, 41, 59, 0.5)" stroke="#94a3b8"
          stroke-width="1.5" />
        <text x="840" y="140" fill="white" font-size="11" font-weight="600" text-anchor="middle">claude</text>
        <text x="840" y="158" fill="#94a3b8" font-size="9" text-anchor="middle">type: anthropic</text>
        <rect x="740" y="194" width="200" height="56" rx="6" fill="#0f172a" />
        <rect x="740" y="194" width="200" height="56" rx="6" fill="rgba(30, 41, 59, 0.5)" stroke="#94a3b8"
          stroke-width="1.5" />
        <text x="840" y="216" fill="white" font-size="11" font-weight="600" text-anchor="middle">litellm &#183;
          gpt4</text>
        <text x="840" y="234" fill="#94a3b8" font-size="9" text-anchor="middle">type: openai</text>
        <rect x="740" y="280" width="200" height="56" rx="6" fill="#0f172a" />
        <rect x="740" y="280" width="200" height="56" rx="6" fill="rgba(30, 41, 59, 0.5)" stroke="#94a3b8"
          stroke-width="1.5" />
        <text x="840" y="302" fill="white" font-size="11" font-weight="600" text-anchor="middle">local</text>
        <text x="840" y="320" fill="#94a3b8" font-size="9" text-anchor="middle">type: ollama (OpenAI-shaped)</text>
      </g>
      <!-- ============================ STORE ============================ -->
      <rect x="200" y="650" width="490" height="190" rx="12" fill="rgba(76, 29, 149, 0.08)" stroke="#a78bfa"
        stroke-width="1" stroke-dasharray="6,4" />
      <text x="212" y="672" fill="#a78bfa" font-size="10" font-weight="600">internal/store &#8212; sqlite event store
        &#183; async writer</text>
      <g>
        <rect x="212" y="684" width="225" height="42" rx="6" fill="#0f172a" />
        <rect x="212" y="684" width="225" height="42" rx="6" fill="rgba(76, 29, 149, 0.4)" stroke="#a78bfa"
          stroke-width="1.5" />
        <text x="222" y="703" fill="white" font-size="10" font-weight="600">requests</text>
        <text x="222" y="718" fill="#94a3b8" font-size="8">rationale &#183; cost &#183; tokens &#183; latency</text>
        <rect x="212" y="736" width="225" height="42" rx="6" fill="#0f172a" />
        <rect x="212" y="736" width="225" height="42" rx="6" fill="rgba(76, 29, 149, 0.4)" stroke="#a78bfa"
          stroke-width="1.5" />
        <text x="222" y="755" fill="white" font-size="10" font-weight="600">content + content_refs</text>
        <text x="222" y="770" fill="#94a3b8" font-size="8">hash-deduped blocks</text>
        <rect x="212" y="788" width="225" height="42" rx="6" fill="#0f172a" />
        <rect x="212" y="788" width="225" height="42" rx="6" fill="rgba(76, 29, 149, 0.4)" stroke="#a78bfa"
          stroke-width="1.5" />
        <text x="222" y="807" fill="white" font-size="10" font-weight="600">affinity_pins</text>
        <text x="222" y="822" fill="#94a3b8" font-size="8">discovery_state &#183; session pins</text>
        <rect x="452" y="684" width="225" height="42" rx="6" fill="#0f172a" />
        <rect x="452" y="684" width="225" height="42" rx="6" fill="rgba(76, 29, 149, 0.4)" stroke="#a78bfa"
          stroke-width="1.5" />
        <text x="462" y="703" fill="white" font-size="10" font-weight="600">client requests</text>
        <text x="462" y="718" fill="#94a3b8" font-size="8">incl. failed + guardrail-rejected</text>
        <rect x="452" y="736" width="225" height="42" rx="6" fill="#0f172a" />
        <rect x="452" y="736" width="225" height="42" rx="6" fill="rgba(76, 29, 149, 0.4)" stroke="#a78bfa"
          stroke-width="1.5" />
        <text x="462" y="755" fill="white" font-size="10" font-weight="600">Arbiter's own calls</text>
        <text x="462" y="770" fill="#94a3b8" font-size="8">classifier calls, same row shape</text>
        <rect x="452" y="788" width="225" height="42" rx="6" fill="#0f172a" />
        <rect x="452" y="788" width="225" height="42" rx="6" fill="rgba(76, 29, 149, 0.4)" stroke="#a78bfa"
          stroke-width="1.5" />
        <text x="462" y="807" fill="white" font-size="10" font-weight="600">sessions (derived)</text>
        <text x="462" y="822" fill="#94a3b8" font-size="8">grouped by session_key / prefix hash</text>
      </g>
      <!-- ============================ ADMIN UI ============================ -->
      <rect x="740" y="650" width="350" height="190" rx="12" fill="rgba(8, 51, 68, 0.12)" stroke="#22d3ee"
        stroke-width="1" stroke-dasharray="6,4" />
      <text x="752" y="672" fill="#22d3ee" font-size="10" font-weight="600">internal/ui &#8212; admin dashboard &#183;
        htmx</text>
      <g>
        <rect x="752" y="684" width="155" height="42" rx="6" fill="#0f172a" />
        <rect x="752" y="684" width="155" height="42" rx="6" fill="rgba(8, 51, 68, 0.4)" stroke="#22d3ee"
          stroke-width="1.5" />
        <text x="762" y="703" fill="white" font-size="10" font-weight="600">Overview</text>
        <text x="762" y="718" fill="#94a3b8" font-size="8">spend &#183; latency</text>
        <rect x="752" y="736" width="155" height="42" rx="6" fill="#0f172a" />
        <rect x="752" y="736" width="155" height="42" rx="6" fill="rgba(8, 51, 68, 0.4)" stroke="#22d3ee"
          stroke-width="1.5" />
        <text x="762" y="755" fill="white" font-size="10" font-weight="600">Sessions</text>
        <text x="762" y="770" fill="#94a3b8" font-size="8">live tail (SSE)</text>
        <rect x="752" y="788" width="155" height="42" rx="6" fill="#0f172a" />
        <rect x="752" y="788" width="155" height="42" rx="6" fill="rgba(8, 51, 68, 0.4)" stroke="#22d3ee"
          stroke-width="1.5" />
        <text x="762" y="807" fill="white" font-size="10" font-weight="600">Discovery</text>
        <text x="762" y="822" fill="#94a3b8" font-size="8">repeated content</text>
        <rect x="922" y="684" width="155" height="42" rx="6" fill="#0f172a" />
        <rect x="922" y="684" width="155" height="42" rx="6" fill="rgba(8, 51, 68, 0.4)" stroke="#22d3ee"
          stroke-width="1.5" />
        <text x="932" y="703" fill="white" font-size="10" font-weight="600">/admin/stats</text>
        <text x="932" y="718" fill="#94a3b8" font-size="8">JSON &#183; providers &#183; epochs</text>
        <rect x="922" y="736" width="155" height="42" rx="6" fill="#0f172a" />
        <rect x="922" y="736" width="155" height="42" rx="6" fill="rgba(8, 51, 68, 0.4)" stroke="#22d3ee"
          stroke-width="1.5" />
        <text x="932" y="755" fill="white" font-size="10" font-weight="600">/admin/reload</text>
        <text x="932" y="770" fill="#94a3b8" font-size="8">hot config reload</text>
        <rect x="922" y="788" width="155" height="42" rx="6" fill="#0f172a" />
        <rect x="922" y="788" width="155" height="42" rx="6" fill="rgba(136, 19, 55, 0.4)" stroke="#fb7185"
          stroke-width="1.5" stroke-dasharray="4,4" />
        <text x="932" y="807" fill="white" font-size="10" font-weight="600">/admin/* gate</text>
        <text x="932" y="822" fill="#94a3b8" font-size="8">forward_auth header</text>
      </g>
      <!-- ============================ LEGEND (outside all boundaries) ============================ -->
      <text x="30" y="868" fill="white" font-size="10" font-weight="600">Legend</text>
      <rect x="30" y="880" width="16" height="10" rx="2" fill="rgba(8, 51, 68, 0.4)" stroke="#22d3ee"
        stroke-width="1" />
      <text x="52" y="889" fill="#94a3b8" font-size="8">Frontend / admin UI</text>
      <rect x="30" y="898" width="16" height="10" rx="2" fill="rgba(6, 78, 59, 0.4)" stroke="#34d399"
        stroke-width="1" />
      <text x="52" y="907" fill="#94a3b8" font-size="8">Backend / pipeline</text>
      <rect x="30" y="916" width="16" height="10" rx="2" fill="rgba(76, 29, 149, 0.4)" stroke="#a78bfa"
        stroke-width="1" />
      <text x="52" y="925" fill="#94a3b8" font-size="8">Database / event store</text>
      <rect x="30" y="934" width="16" height="10" rx="2" fill="rgba(30, 41, 59, 0.5)" stroke="#94a3b8"
        stroke-width="1" />
      <text x="52" y="943" fill="#94a3b8" font-size="8">External / clients + providers</text>
      <rect x="200" y="880" width="16" height="10" rx="2" fill="rgba(136, 19, 55, 0.4)" stroke="#fb7185"
        stroke-width="1" />
      <text x="222" y="889" fill="#94a3b8" font-size="8">Security / guardrail</text>
      <rect x="200" y="898" width="16" height="10" rx="2" fill="transparent" stroke="#fbbf24" stroke-width="1"
        stroke-dasharray="4,3" />
      <text x="222" y="907" fill="#94a3b8" font-size="8">Boundary: app surface</text>
      <line x1="200" y1="921" x2="216" y2="921" stroke="#fb7185" stroke-width="1" stroke-dasharray="4,3" />
      <text x="222" y="925" fill="#94a3b8" font-size="8">Classifier model call</text>
      <rect x="200" y="934" width="16" height="10" rx="2" fill="transparent" stroke="#fb7185" stroke-width="1"
        stroke-dasharray="4,3" />
      <text x="222" y="943" fill="#94a3b8" font-size="8">Gated surface: /admin/*</text>
    </svg>
  </div>
  <!-- Info Cards -->
  <div class="cards">
    <div class="card">
      <div class="card-header">
        <div class="card-dot emerald"></div>
        <h3>Request lifecycle</h3>
      </div>
      <ul>
        <li>&#8226; Normalize first: every wire format becomes Normalized*</li>
        <li>&#8226; Session key + as-sent capture taken before guardrails</li>
        <li>&#8226; Classifiers fill axes; policy rules match on the axes</li>
        <li>&#8226; Upstream failure &#8594; cooldown &#8594; fallback route</li>
      </ul>
    </div>
    <div class="card">
      <div class="card-header">
        <div class="card-dot violet"></div>
        <h3>Nothing invisible</h3>
      </div>
      <ul>
        <li>&#8226; One requests row per request, with routing rationale</li>
        <li>&#8226; Arbiter's own classifier calls are rows too</li>
        <li>&#8226; Guardrail refusals get a real row, not a stub</li>
        <li>&#8226; Content hash-deduped in content / content_refs</li>
      </ul>
    </div>
    <div class="card">
      <div class="card-header">
        <div class="card-dot amber"></div>
        <h3>Design boundaries</h3>
      </div>
      <ul>
        <li>&#8226; Single user, single-digit concurrency by design</li>
        <li>&#8226; No in-app auth: tailnet + forward_auth proxy</li>
        <li>&#8226; Hub-and-spoke: one spoke per wire format, not N&#215;N</li>
        <li>&#8226; Memory, search, MCP stay external over HTTP</li>
      </ul>
    </div>
  </div>
  <!-- Footer -->
  <p class="footer">
    Arbiter &#8226; cmd/arbiter &#8594; internal/{http,pipeline,router,classifier,translator,upstream,store,ui} &#183;
    /v1/responses not built
  </p>
</div>
</div>
