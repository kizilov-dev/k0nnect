<div align="center">

<img src="website/assets/logo.svg" width="72" height="72" alt="k8nnect">

# k8nnect

**Свой сервер. Тот же Kubernetes.**

Подключение внешней VDS как полноценного Worker через SSH и WireGuard.
CLI для установки, аддон для поддержания соединения.

[Сайт](https://kizilov-dev.github.io/k8nnect/) · [Быстрый старт](#быстрый-старт) · [Документация](https://kizilov-dev.github.io/k8nnect/docs/) · [Архитектура](docs/architecture.md) · [Результаты испытаний](docs/worker-enrollment-2026-10-30.md)

</div>

---

k8nnect устанавливает ПО на внешнем Linux-сервере, создаёт защищённую связь с
существующими Workers и регистрирует новый kubelet в **том же кластере**. После
проверок сервер участвует в обычном планировании Pod.

Рабочий MVP проверен на **k0s 1.36.4 + Calico 3.32.2, IPv4 VXLAN**. Подключение
независимых кластеров — отдельный, пока не реализованный режим. Поддержка других
CNI не заявляется.

## Что делает утилита

- Проверяет совместимость кластера и закреплённый SSH-ключ VDS.
- Устанавливает WireGuard, k0s worker и systemd-сервисы.
- Запускает gateway-агенты на существующих Workers.
- Регистрирует ноду через краткоживущий bootstrap token, затем отзывает его.
- Проверяет планирование, TCP/UDP с каждым существующим Ready Worker, Service DNS, logs и exec.
- Снимает начальный NoSchedule taint только после успешных проверок.
- В режиме аддона поддерживает конфигурацию и сообщает состояние через ConfigMap.

Физические интерфейсы сервера сохраняют свои адреса. Kubelet и containerd работают
в отдельном network namespace; Calico остаётся владельцем Pod-маршрутов и IPAM.

## Требования

| Компонент | Поддерживаемая конфигурация |
| --- | --- |
| Кластер | k0s, Linux/amd64 Workers, одинаковая minor-версия Kubernetes |
| CNI | Calico, IPv4, VXLAN Always, IPIP Never, `IP_AUTODETECTION_METHOD=interface=eth1` |
| VDS | Linux/amd64, root SSH, systemd, apt, Python 3, поддержка WireGuard в ядре |
| Доступ | kubeconfig с правами установки RBAC, gateway-агентов и worker bootstrap |
| Сеть | Непересекающаяся приватная /24, UDP к gateway и VDS, доступ к пакетам и образам |
| Сборка | Go ≥ 1.26, Linux; локальному CLI нужен `kubectl` |

SSH на control plane не требуется, если API разрешает стандартный worker bootstrap.
Первый установщик поддерживает **одну внешнюю VDS на кластер**. Автоматического
переключения основного gateway пока нет.

## Быстрый старт

### 1. Собрать

```sh
git clone https://github.com/kizilov-dev/k8nnect.git
cd k8nnect
make build
make plugin                       # bin/kubectl-k8nnect
export PATH="$PWD/bin:$PATH"
```

### 2. Подготовить настройки

Скопируйте [examples/connection.json](examples/connection.json) за пределы
репозитория. Укажите адрес VDS и проверенный **Ed25519 fingerprint** SSH-сервера.
`passwordFile` или `privateKeyFile` должен ссылаться на локальный файл с правами
`0600`. Не храните пароль в JSON. Адрес `203.0.113.10` в примере — заглушка.

```sh
cp examples/connection.json /secure/connection.json
chmod 600 /secure/connection.json
```

### 3. Проверить без изменений

```sh
k8nnect connect-check --kubeconfig /secure/cluster.yaml --config /secure/connection.json
k8nnect connect-plan --kubeconfig /secure/cluster.yaml --config /secure/connection.json \
  --output /secure/connect-review.json
```

`connect-check` читает состояние API и VDS через SSH. `connect-plan` создаёт
обезличенный план локально, отправляет только GET в реальный API и не использует
SSH. План предназначен для ревью, а не для `kubectl apply`.

### 4. Установить аддон

```sh
k8nnect addon-install --kubeconfig /secure/cluster.yaml --config /secure/connection.json
kubectl --kubeconfig /secure/cluster.yaml -n k8nnect-system logs -f deployment/installer
kubectl --kubeconfig /secure/cluster.yaml get node k8nnect-vds -o wide
```

Для однократного подключения вместо аддона используйте `k8nnect connect` с теми
же аргументами. Все команды также доступны как `kubectl k8nnect …`.

## Команды

| Команда | Результат |
| --- | --- |
| `connect-check` | Читающая проверка совместимости и SSH |
| `connect-plan --output PATH` | Локальный план с удалёнными значениями секретов |
| `connect` | Установка и подключение VDS с проверкой workloads |
| `addon-install` | Установка непрерывно работающего аддона |
| `verify-node` | Повторная проверка Pod, TCP/UDP, DNS, logs и exec |
| `disconnect --drain` | Drain с учётом PDB и удаление принадлежащего соединению |

Все команды принимают `--config PATH --kubeconfig PATH`. Без `--kubeconfig`
используется ServiceAccount внутри кластера. Профиль статического kubeadm/Flannel
с командами `plan`, `apply` и `agent` описан в [отдельном runbook](docs/runbook.md).

## Проверено на живом стенде

VDS стала Ready-нодой; прошли планирование Pod, TCP 64 КиБ и UDP
1000/1422/8192 байт в обе стороны со всеми тремя исходными Workers, включая Worker
без ExternalIP. Проверены Service DNS, kubelet logs/exec, повторная сверка аддона
и перезапуск установленных сервисов. [Подробный отчёт](docs/worker-enrollment-2026-10-30.md).

Это результат для конкретного профиля, а не сертификация любых кластеров.
Полная перезагрузка VDS, NetworkPolicy, CSI и длительная нагрузка не проверены.

## Права и секреты

Gateway использует hostNetwork и NET_ADMIN. Аддон сохраняет SSH-креды в Kubernetes
Secret `vds-ssh` и получает явный набор RBAC-прав, включая bootstrap и проверяемое
одобрение kubelet-serving CSR. Административный kubeconfig на VDS не передаётся.
Шифрование Secret на диске зависит от настроек кластера. Перед установкой
прочитайте [границы прав и сетевые ограничения](docs/addon.md).

## Разработка

```sh
make check                         # go vet + race tests
make site                          # статический сайт в site/
python3 -m http.server 8080 --directory site
```

Интеграционные тесты, изменяющие инфраструктуру, запускаются отдельно и только
на подготовленном стенде: [test/integration](test/integration).
Сайт собирается из `website/` и публикуется в GitHub Pages через Actions.
Локальные секреты и исходные снимки стенда исключены из публикации.
