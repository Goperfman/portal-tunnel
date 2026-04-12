<script lang="ts">
	import HeroSection from '$lib/components/landing/HeroSection.svelte';
	import DifferentiatorCarousel from '$lib/components/landing/DifferentiatorCarousel.svelte';
	import CoreFeaturesGrid from '$lib/components/landing/CoreFeaturesGrid.svelte';
	import TunnelCommandForm from '$lib/components/landing/TunnelCommandForm.svelte';
</script>

<svelte:head>
	<title>Portal — Expose Local Apps to the Public Internet</title>
	<meta
		name="description"
		content="Portal lets you expose local services to the public internet through secure, trustless relay tunnels with end-to-end TLS encryption."
	/>
</svelte:head>

<div class="not-prose">
	<HeroSection />
	<DifferentiatorCarousel />
	<CoreFeaturesGrid />
	<TunnelCommandForm />
</div>
