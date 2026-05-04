# Extended Stress Test Specification for MOLS-EWMA Policy

This document outlines long-term stress test scenarios designed to validate the stability, scalability, and transposition accuracy of the EWMA-based relay selection policy.

## 1. Scenario A: "The Messy Grid" (64x64 Non-Ideal Distribution)
**Objective:** Validate transposition logic when relay density is non-optimal (not a perfect divisor of 64), which naturally occurs in fragmented network topologies.

- **Setup:** 53 Relay URLs (not a power of 2, creating uneven GF(64) hash mapping).
- **Network Stress:** 
    - Inject "Micro-burst" RTT spikes (100ms duration, 800ms magnitude) every 30 seconds to 30% of nodes.
    - Validate that EWMA ($\alpha=0.3$) filters these spikes, preventing frequent priority flapping.
- **Success Criteria:** 
    - The system must prioritize the remaining 70% of stable nodes.
    - No "Priority Oscillation" where a node bounces between rank 1 and 5 every minute.

## 2. Scenario B: "Massive Scale" (256 Node Pool)
**Objective:** Validate performance and memory stability when the relay set significantly exceeds the standard 64-node MOLS grid.

- **Setup:** 256 active Relay URLs.
- **Network Stress:**
    - Perform a "Rolling Congestion" simulation: shift a 500ms+ latency penalty across groups of 32 nodes sequentially over a 24-hour period.
- **Success Criteria:**
    - **Latency:** `rankRelayPool` execution time must remain < 5ms under the increased load.
    - **Stability:** The transposition logic should maintain a consistent set of the "top 3" healthiest nodes even as congestion rolls across the 256-node pool.
    - **Memory:** Telemetry counters (Prometheus gauge tracking) must not grow beyond the defined `boundedRelay` limit of 1024.

## 3. Implementation Guidelines for `portal-loadtest`
- **Simulation Duration:** All scenarios should run for a minimum of 24 hours to observe EWMA convergence.
- **Telemetry Hook:** Integrate with the `portal/telemetry` package to log the `chi-square` uniformity metric every hour alongside the `EWMA RTT` distribution.
- **Command:**
  ```bash
  # Scenario A
  make load-test -- -clients 500 -relays 53 -mode messy
  
  # Scenario B
  make load-test -- -clients 2000 -relays 256 -mode scale
  ```
