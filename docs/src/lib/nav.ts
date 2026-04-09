export interface NavItem {
	title: string;
	href: string;
}

export interface NavSection {
	title: string;
	badge?: string;
	items: NavItem[];
}

export const navigation: NavSection[] = [
	{
		title: 'Getting Started',
		items: [
			{ title: 'Quick Start', href: '/getting-started' },
			{ title: 'Concepts', href: '/concepts' },
			{ title: 'Self-Hosting', href: '/self-hosting' }
		]
	},
	{
		title: 'Guides',
		items: [{ title: 'TCP/UDP Tunneling', href: '/tcp-udp-tunneling' }]
	},
	{
		title: 'Reference',
		items: [
			{ title: 'CLI Reference', href: '/cli-reference' },
			{ title: 'API Reference', href: '/api-reference' },
			{ title: 'Configuration', href: '/configuration' }
		]
	},
	{
		title: 'Advanced',
		items: [
			{ title: 'Architecture', href: '/architecture' },
			{ title: 'Deployment', href: '/deployment' }
		]
	}
];
