// Texts of the landing page and the footer in both languages. Docs pages live in
// src/content/docs (Russian) and src/content/docs/en (English).

export const links = {
	github: 'https://github.com/o0hhs/arikan',
	releases: 'https://github.com/o0hhs/arikan/releases',
	issues: 'https://github.com/o0hhs/arikan/issues',
	license: 'https://github.com/o0hhs/arikan/blob/main/LICENSE',
	sponsor: 'https://web.tribute.tg/d/REA',
	mihomo: 'https://github.com/MetaCubeX/mihomo',
};

// Two lines, so the command fits the hero without wrapping in odd places; bash reads the
// trailing backslash as one command when it is pasted.
export const installCommand =
	'curl -fsSL https://github.com/o0hhs/arikan/releases/latest/download/install.sh \\\n  | sudo bash';

export type Lang = 'ru' | 'en';

const docs = (lang: Lang, path: string) => `${import.meta.env.BASE_URL}${lang === 'en' ? 'en/' : ''}${path}`;

export const strings = {
	ru: {
		hero: {
			title: 'Своя VPN-панель на ядре mihomo',
			lead: 'Протоколы, ноды, подписки и Telegram-бот с оплатой в одной панели. Ставится одной командой, сама переживает блокировки. Бесплатная, с открытым кодом под GPL-3.0.',
			requirements: 'Ubuntu 22.04+ или Debian 12+, amd64 или arm64. Установщик сам ставит Docker и в конце выдаёт ссылку на админку.',
			primary: 'Установить',
			secondary: 'Как это устроено',
			art: 'Логотип Arikan',
		},
		features: {
			title: 'Что внутри',
			items: [
				{
					title: 'Ядро mihomo внутри ноды',
					text: 'mihomo встроен как библиотека, поэтому трафик считается по каждому соединению каждого пользователя. Тарифы по сроку и гигабайтам, лимит устройств и привязка к ним.',
					href: docs('ru', 'start/overview/'),
				},
				{
					title: 'Пятнадцать протоколов',
					text: 'VLESS REALITY с Vision, XHTTP и gRPC, Hysteria2, TUIC, AnyTLS, Trojan, TrustTunnel, Mieru и другие. Каждый добавляется из пресета, ключи создаются сами.',
					href: docs('ru', 'protocols/presets/'),
				},
				{
					title: 'Сама переживает блокировки',
					text: 'Если клиенты из разных сетей перестали доходить до протокола, панель переносит его на свободный HTTPS-порт или подбирает новый сайт маскировки рядом с сервером.',
					href: docs('ru', 'protocols/autotune/'),
				},
				{
					title: 'Ноды, каскады и WARP',
					text: 'Удалённая нода ставится одной командой с ключом и обновляется из панели. Протокол может выходить в интернет через другую ноду или через Cloudflare WARP.',
					href: docs('ru', 'nodes/nodes/'),
				},
				{
					title: 'Подписка под каждое приложение',
					text: 'Подписка узнаёт Happ, v2RayTun, Koala Clash, Clash Verge, Hiddify и другие и отдаёт каждому только то, что оно запускает. Для Clash есть маршруты по сервисам.',
					href: docs('ru', 'subscriptions/page/'),
				},
				{
					title: 'Telegram-бот и Mini App',
					text: 'Подписчики смотрят срок и устройства, покупают и продлевают доступ. Оплата через Telegram Stars и адаптеры маркетплейса: ЮKassa, CryptoBot.',
					href: docs('ru', 'billing/telegram/'),
				},
				{
					title: 'Промокоды, пулы и пакеты',
					text: 'Скидки, бонусные дни и трафик. Отдельный лимит для части протоколов, докупка гигабайтов в боте, пробный период.',
					href: docs('ru', 'billing/promo-codes/'),
				},
				{
					title: 'Переезд с других панелей',
					text: 'Импорт пользователей из Marzban, PasarGuard и Remnawave. Старые ссылки подписок работают, когда старый домен указывает на Arikan.',
					href: docs('ru', 'operations/import/'),
				},
				{
					title: 'Спокойные обновления',
					text: 'PostgreSQL, подписанные релизы, бэкап перед каждым обновлением и откат, если новая версия не поднялась. Бэкап базы может каждый день приходить в Telegram.',
					href: docs('ru', 'operations/updates/'),
				},
			],
		},
		protocols: {
			title: 'Каждому приложению то, что оно запускает',
			lead: 'Подписка определяет приложение и не отдаёт ему протоколы, которые оно не поднимет. Никаких «у меня не работает» из-за неподходящего ключа.',
			headers: ['Протокол', 'Clash-приложения', 'Happ, v2RayTun', 'Hiddify, Karing'],
			yes: 'есть',
			no: 'нет',
			note: '¹ Один ключ на всех: учёта и лимитов по пользователям на этих протоколах нет.',
			more: 'Все пресеты и их настройки',
			moreHref: docs('ru', 'protocols/presets/'),
		},
		phones: {
			title: 'Страница подписки и Mini App',
			text: 'Подписчик открывает ссылку в браузере или в Telegram и видит срок, трафик и устройства. Платформа определяется сама, а кнопка «Добавить» открывает подписку сразу в приложении: Happ, v2RayTun, Koala Clash, Hiddify и другие.',
			link: 'Про подписки и приложения',
			href: docs('ru', 'subscriptions/page/'),
			alts: ['Страница подписки на телефоне', 'Обзор панели на телефоне'],
		},
		funding: {
			badge: 'Идёт сбор на новые функции',
			title: 'Сборы на функции',
			lead: 'Функции, которые просит сообщество, мы делаем, когда на них собрана сумма. Выберите нужную и поддержите её.',
			all: 'Все цели',
		},
		cta: {
			title: 'Поставить на свой сервер',
			text: 'Одна команда на чистом сервере. Дальше всё делается в панели, а команда arikan открывает меню управления.',
			docs: 'Читать про установку',
			sponsorTitle: 'Поддержать проект',
			sponsorText: 'Arikan — бесплатный, с открытым кодом. Если панель вам пригодилась, автора можно поддержать.',
			sponsor: 'Поддержать на Tribute',
			goals: 'На что собираем',
			github: 'Код на GitHub',
		},
		header: {
			label: 'Проект',
			githubLabel: 'GitHub: исходный код Arikan',
			support: 'Поддержать',
			supportLabel: 'Поддержать проект на Tribute',
			otherLanguage: 'English version',
		},
		footer: {
			label: 'Ссылки проекта',
			license: 'Arikan — неофициальный форк mikan, свободная программа под GNU GPL v3. Внутри mihomo (GPL-3.0).',
			releases: 'Релизы',
			issues: 'Сообщить о проблеме',
			sponsor: 'Поддержать',
		},
	},
	en: {
		hero: {
			title: 'Your own VPN panel on the mihomo core',
			lead: 'Protocols, nodes, subscriptions and a Telegram bot that takes payments, in one panel. One command to install, survives blocking on its own. Free and open source under GPL-3.0.',
			requirements: 'Ubuntu 22.04+ or Debian 12+, amd64 or arm64. The installer sets up Docker by itself and prints your admin link at the end.',
			primary: 'Install',
			secondary: 'How it works',
			art: 'The Arikan logo',
		},
		features: {
			title: 'What is inside',
			items: [
				{
					title: 'mihomo inside the node',
					text: 'mihomo is embedded as a library, so traffic is counted per user on every connection. Plans by time and gigabytes, device limits and device binding.',
					href: docs('en', 'start/overview/'),
				},
				{
					title: 'Fifteen protocols',
					text: 'VLESS REALITY with Vision, XHTTP and gRPC, Hysteria2, TUIC, AnyTLS, Trojan, TrustTunnel, Mieru and more. Each one comes from a preset with keys generated for you.',
					href: docs('en', 'protocols/presets/'),
				},
				{
					title: 'Survives blocking on its own',
					text: 'When clients from different networks stop reaching a protocol, the panel moves it to a free HTTPS port or picks a new camouflage site next to the server.',
					href: docs('en', 'protocols/autotune/'),
				},
				{
					title: 'Nodes, cascades and WARP',
					text: 'A remote node installs with one command and a key, and updates from the panel. A protocol can leave through another node or through Cloudflare WARP.',
					href: docs('en', 'nodes/nodes/'),
				},
				{
					title: 'A subscription for every app',
					text: 'The subscription recognises Happ, v2RayTun, Koala Clash, Clash Verge, Hiddify and others and gives each only what it runs. Clash apps get routing by service.',
					href: docs('en', 'subscriptions/page/'),
				},
				{
					title: 'Telegram bot and Mini App',
					text: 'Subscribers check their term and devices, buy and renew. Payments through Telegram Stars and marketplace adapters: YooKassa, CryptoBot.',
					href: docs('en', 'billing/telegram/'),
				},
				{
					title: 'Promo codes, pools and packages',
					text: 'Discounts, bonus days and traffic. A separate limit for some protocols, extra gigabytes sold in the bot, a free trial.',
					href: docs('en', 'billing/promo-codes/'),
				},
				{
					title: 'Moving from other panels',
					text: 'Import users from Marzban, PasarGuard and Remnawave. Old subscription links keep working once the old domain points to Arikan.',
					href: docs('en', 'operations/import/'),
				},
				{
					title: 'Calm updates',
					text: 'PostgreSQL, signed releases, a backup before every update and a rollback if the new version does not come up. A database backup can arrive in Telegram daily.',
					href: docs('en', 'operations/updates/'),
				},
			],
		},
		protocols: {
			title: 'Every app gets what it can run',
			lead: 'The subscription recognises the app and never hands it a protocol it cannot start, so nobody is left with a key that does not work.',
			headers: ['Protocol', 'Clash apps', 'Happ, v2RayTun', 'Hiddify, Karing'],
			yes: 'yes',
			no: 'no',
			note: '¹ One key for all users: no per-user accounting or limits on these.',
			more: 'All presets and their settings',
			moreHref: docs('en', 'protocols/presets/'),
		},
		phones: {
			title: 'Subscription page and Mini App',
			text: 'A subscriber opens the link in a browser or in Telegram and sees the term, traffic and devices. The platform is detected, and the Add button opens the subscription right in the app: Happ, v2RayTun, Koala Clash, Hiddify and more.',
			link: 'About subscriptions and apps',
			href: docs('en', 'subscriptions/page/'),
			alts: ['The subscription page on a phone', 'The panel overview on a phone'],
		},
		funding: {
			badge: 'Collecting for new features',
			title: 'Funding features',
			lead: 'Features the community asks for get built once their sum is collected. Pick the one you need and support it.',
			all: 'All goals',
		},
		cta: {
			title: 'Put it on your server',
			text: 'One command on a fresh server. Everything else happens in the panel, and the arikan command opens the management menu.',
			docs: 'Read about the install',
			sponsorTitle: 'Support the project',
			sponsorText: 'Arikan is free and open source. If the panel is useful to you, you can support its author.',
			sponsor: 'Support on Tribute',
			goals: 'What we collect for',
			github: 'Code on GitHub',
		},
		header: {
			label: 'Project',
			githubLabel: 'GitHub: the Arikan source code',
			support: 'Support',
			supportLabel: 'Support the project on Tribute',
			otherLanguage: 'Русская версия',
		},
		footer: {
			label: 'Project links',
			license: 'Arikan is an unofficial fork of mikan, free software under the GNU GPL v3. It embeds mihomo (GPL-3.0).',
			releases: 'Releases',
			issues: 'Report a problem',
			sponsor: 'Sponsor',
		},
	},
} as const;

// From README.md: Clash apps (mihomo core), Xray apps, sing-box apps.
export const protocolMatrix: [string, boolean, boolean, boolean, boolean?][] = [
	['VLESS · REALITY · Vision', true, true, true],
	['VLESS · REALITY · XHTTP', true, true, false],
	['VLESS · REALITY · gRPC', true, true, true],
	['VLESS PQ', true, true, false],
	['Trojan · REALITY', true, true, true],
	['Hysteria2', true, true, true],
	['TUIC v5', true, false, true],
	['AnyTLS', true, false, true],
	['TrustTunnel', true, false, false],
	['ShadowQUIC', true, false, false],
	['Mieru', true, false, false],
	['Shadowsocks-2022', true, true, true, true],
	['Sudoku', true, false, false, true],
	['Snell', true, false, false, true],
	['VMess', true, true, true],
];
