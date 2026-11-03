# Эксплуатация

В примерах `kubectl` использует администраторский kubeconfig. Команды CLI принимают `--config` и `--kubeconfig`; их файлы лежат в локальном каталоге `/secure`.

## Смотреть состояние

```sh
kubectl get vdsconnection
kubectl get node k0nnect-vds -o wide
kubectl -n k0nnect-system get pods -o wide
```

`Phase` равна `Connecting` во время сверки, `Ready` после успешной сверки, `Disconnecting` во время отключения и `Error` с причиной в `Message`. Контроллер сверяет подключение при изменении объекта и примерно раз в минуту — например, чтобы добавить gateway на новые Workers.

## Смотреть логи

```sh
kubectl -n k0nnect-system logs deployment/controller --tail=100
kubectl -n k0nnect-system logs -l app=k0nnect-gateway --tail=50 --prefix
```

На VDS:

```sh
systemctl status k0nnect-network k0nnect-agent k0sworker
journalctl -u k0sworker -u k0nnect-agent --since '10 minutes ago'
```

Не публикуйте bootstrap-файлы, kubeconfig, приватные ключи или необезличенные снимки конфигурации. `wg show` скрывает private key; `wg show all dump` может раскрыть его.

## Повторить workload-проверки

```sh
k0nnect verify-node \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

Проверка создаёт собственные временные Pod, Services и Jobs. Она тестирует новую ноду с каждым существующим Ready Worker и удаляет ресурсы после успеха. Пока CNI может ещё настраивать новую ноду (для kube-router — до `--routes-sync-period` плюс минута, для остальных — минута), неудачная попытка повторяется. При окончательной ошибке тестовые ресурсы остаются для диагностики; следующий прогон их очищает.

Повторная сверка поддерживает уже управляемую сеть. Рабочая проверенная нода повторно не присоединяется. Если `k0sworker` на VDS остановлен, нода регистрируется и проверяется заново, даже когда API ещё показывает её Ready.

## Отключить Worker

С контроллером:

```sh
kubectl delete vdsconnection vds
```

Finalizer `k0nnect.io/disconnect` держит объект, пока не закончится очистка. Контроллер делает cordon/drain ноды с учётом PodDisruptionBudget, по SSH останавливает её сервисы и containerd-shim, удаляет локальное состояние CNI, gateway, RBAC, bootstrap token и worker-профили. Если на VDS так и не были запущены Worker и сеть — например, из-за неверного адреса, — удаляются только объекты кластера.

После однократного подключения через CLI:

```sh
k0nnect disconnect --drain \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

Локальное состояние CNI на VDS — `/etc/cni/net.d`, `/var/lib/cni`, `/run/flannel`, состояние Calico, Cilium (включая BPF-пины) и kube-router — удаляется: при повторном подключении нода получает новую Pod CIDR, и устаревшее состояние помешало бы создавать Pod. DaemonSet CNI заново создаст всё нужное.

Пакеты, данные k0s и ключи восстановления на VDS сохраняются. Полный `k0s reset` не выполняется. Цикл `disconnect --drain` → `connect` через CLI проверен на всех четырёх поддержанных CNI.

## Обновление и удаление

Чтобы обновить контроллер, примените `install.yaml` нового релиза; gateway получат новый образ при следующей сверке. Менять транспортный адрес подключённой ноды без отключения нельзя. Автоматического обновления Kubernetes и failover основного gateway нет.

Чтобы удалить k0nnect, сначала удалите VDSConnection и дождитесь его исчезновения, затем выполните `kubectl delete -f install.yaml`.

## Переход с addon-install или CLI

Команды `addon-install` и `operator` заменены контроллером, а ConfigMap `connection-status` — статусом VDSConnection. Сначала отключите такую установку командой `k0nnect disconnect --drain` (она же останавливает прежний Deployment установщика), затем установите контроллер. То же нужно сделать для сервера, подключённого через `k0nnect connect`: контроллер не трогает объекты чужого соединения.

## Переход со старого имени

Переименование меняет метки владения, systemd-сервисы, каталоги и сетевые namespace. Сохраните старый бинарник и настройки: сначала выполните drain/disconnect прежней версией, затем установите k0nnect по [быстрому старту](getting-started.md). Новая версия отказывается подключаться при обнаружении старого управляемого namespace. Действующие ноды автоматически не перенастраиваются.
