package api

// defaultInstruction is the text a platform starts with. It is only a seed: the
// operator edits it in the panel, and from then on the stored version wins. The
// personal values arrive through placeholders, so the same text serves everyone.
func defaultInstruction(platform string) string {
	switch platform {
	case "android_dot":
		return `## Android — «Частный DNS»

1. Откройте **Настройки → Сеть и интернет → Частный DNS**.
2. Выберите «Имя хоста поставщика частного DNS».
3. Введите: ` + "`{{dot_host}}`" + `
4. Сохраните и вернитесь назад.

Проверить: откройте любой сайт — если страницы грузятся, всё настроено.

> Android принимает только имя хоста. За ним стоят все входные ноды, поэтому
> при недоступности одной устройство само перейдёт на другую.`

	case "apple_doh", "apple_dot":
		return `## iPhone, iPad и Mac

1. Нажмите **«Скачать»** выше — загрузится файл настройки.
2. **iPhone/iPad:** Настройки → в самом верху появится «Профиль загружен» → откройте → **Установить**.
   **Mac:** Системные настройки → Основные → VPN и профили устройств → установите профиль.
3. Готово — шифрованный DNS включится сразу.

Проверить: откройте любой сайт. Чтобы отключить — удалите профиль там же.

> Профиль привязан к этому устройству. Для второго устройства добавьте ещё одно
> на этой странице, чтобы их можно было отключать по отдельности.`

	case "windows_doh":
		return `## Windows 11

1. **Параметры → Сеть и Интернет** → выберите активное подключение → **Изменить назначение DNS-серверов**.
2. Переключите на **«Вручную»**, включите **IPv4**.
3. Предпочитаемый DNS: ` + "`{{ingress_ipv4}}`" + `
4. Шифрование DNS: **«Только зашифрованный (DNS over HTTPS)»**.
5. Шаблон DoH: ` + "`{{doh_url}}`" + `
6. Сохраните.

> В Windows 10 задать произвольный DoH-шаблон в интерфейсе нельзя — используйте
> роутер или другое устройство.`

	case "router":
		return `## Роутер (OpenWrt)

` + "```sh" + `
opkg update && opkg install https-dns-proxy
uci set https-dns-proxy.@https-dns-proxy[0].resolver_url='{{doh_url}}'
uci commit https-dns-proxy && /etc/init.d/https-dns-proxy restart
` + "```" + `

Альтернатива — DoT через stubby, имя хоста ` + "`{{dot_host}}`" + `.

> Настройка на роутере покрывает все устройства в сети сразу, но тогда они
> неотличимы друг от друга в статистике.`

	default:
		return `## Обычный DNS

Адреса: ` + "`{{ingress_ipv4}}`" + `

> Обычный DNS идёт без шифрования — провайдер видит, какие сайты вы
> спрашиваете. Используйте его только там, где устройство не умеет DoH или DoT.`
	}
}
