<script lang="ts">
	const features = [
		{
			eyebrow: 'HTTPS',
			title: 'Public HTTPS for localhost',
			description:
				'Publish local apps through TCP passthrough without opening inbound ports.'
		},
		{
			eyebrow: 'TLS',
			title: 'End-to-end TLS on your side',
			description:
				'Tenant TLS terminates locally with MITM detection, so relays cannot access plaintext.'
		},
		{
			eyebrow: 'Setup',
			title: 'One-command setup',
			description:
				'Start relays and tunnels with minimal setup and a short copy-paste path.'
		},
		{
			eyebrow: 'Relay',
			title: 'Self-hosted relays and pools',
			description:
				'Connect to public relays, use discovered relays as a pool with failover, or run your own.'
		},
		{
			eyebrow: 'Transport',
			title: 'Raw TCP with optional UDP',
			description:
				'Carry web traffic and arbitrary protocols without SSH or WebSocket overlays.'
		},
		{
			eyebrow: 'Identity',
			title: 'SIWE ownership with ENS support',
			description:
				'Authenticate ownership with SIWE and keep identity portable with ENS-based naming support.'
		}
	] as const;
</script>

<div class="relative -mx-4 w-auto sm:-mx-6 md:-mx-8">
	<div class="mx-auto max-w-6xl px-4 pb-5 text-left sm:px-6">
		<div class="space-y-2">
			<p
				class="text-sm font-semibold uppercase tracking-[0.3em]"
				style="color: var(--neon-cyan);"
			>
				Core features
			</p>
			<h2
				class="text-3xl font-semibold tracking-tight"
				style="color: var(--foreground);"
			>
				Built for real localhost publishing
			</h2>
		</div>
	</div>
	<div class="overflow-hidden border-t" style="border-color: var(--border); background: color-mix(in oklch, var(--border) 70%, transparent);">
		<div class="grid gap-px sm:grid-cols-2 lg:grid-cols-3">
			{#each features as { eyebrow, title, description } (title)}
				<article
					class="flex min-h-52 bg-background/[0.88] p-6 text-left transition-colors duration-200 hover:bg-background/[0.92] sm:min-h-56 sm:p-7"
				>
					<div class="flex h-full flex-col space-y-3">
						<p
							class="text-[11px] font-semibold uppercase tracking-[0.24em] text-primary/80"
						>
							{eyebrow}
						</p>
						<h3
							class="text-[1.2rem] font-semibold tracking-tight text-foreground sm:text-[1.32rem] sm:leading-tight"
						>
							{title}
						</h3>
						<p
							class="max-w-[28ch] text-[0.95rem] leading-6 text-text-muted"
						>
							{description}
						</p>
						<div class="mt-auto pt-4">
							<div
								class="h-px w-12"
								style="background: var(--gradient-primary-fade);"
							></div>
						</div>
					</div>
				</article>
			{/each}
		</div>
	</div>
</div>
