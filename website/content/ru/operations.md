# Эксплуатация

CLI принимает `--config` и `--kubeconfig`. В примерах ниже kubeconfig и connection JSON находятся в локальном каталоге `/secure`.

## Смотреть состояние

```sh
kubectl --kubeconfig /secure/cluster.yaml get node k0nnect-vds -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system get pods -o wide
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system \
  get configmap connection-status -o yaml
```

ConfigMap содержит `phase`, имя ноды, время и краткое описание. `Connecting` означает выполняющуюся сверку, `Ready` — успешное завершение цикла, `Error` — необходимость диагностики.

## Смотреть логи

```sh
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system \
  logs deployment/installer --tail=100
kubectl --kubeconfig /secure/cluster.yaml -n k0nnect-system \
  logs -l app=k0nnect-gateway --tail=50 --prefix
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

Повторный `connect` или reconcile аддона поддерживает уже управляемую сеть. Рабочая проверенная нода повторно не присоединяется. Если `k0sworker` на VDS остановлен, нода регистрируется и проверяется заново, даже когда API ещё показывает её Ready.

## Отключить Worker

```sh
k0nnect disconnect --drain \
  --kubeconfig /secure/cluster.yaml \
  --config /secure/connection.json
```

Команда останавливает установщик, cordon/drain новой ноды с учётом PodDisruptionBudget, останавливает принадлежащие ей сервисы и удаляет собственную сеть, Node и кластерные ресурсы соединения.

На VDS также завершаются оставшиеся containerd-shim k0s и удаляется локальное состояние CNI этой ноды: `/etc/cni/net.d`, `/var/lib/cni`, `/run/flannel`, состояние Calico, Cilium (включая BPF-пины) и kube-router. При повторном подключении нода получает новую Pod CIDR, и устаревшее состояние помешало бы создавать Pod. DaemonSet CNI заново создаст всё нужное.

Пакеты, данные k0s и ключи восстановления на VDS сохраняются. Полный `k0s reset` не выполняется. Цикл `disconnect --drain` → `connect` проверен на всех четырёх поддержанных CNI.

## Обновление и ограничения

После сборки новой версии повторный `addon-install` обновляет установщик. Менять транспортный адрес уже подключённой ноды без явного отключения нельзя. Первичная реализация не предоставляет автоматического обновления Kubernetes и failover основного gateway.

## Переход со старого имени

Переименование меняет метки владения, systemd-сервисы, каталоги и сетевые namespace. Сохраните старый бинарник и настройки: сначала выполните drain/disconnect прежней версией, затем обновите имя ноды и namespace в конфигурации и установите k0nnect. Новая версия отказывается подключаться при обнаружении старого управляемого namespace. Действующие ноды автоматически не перенастраиваются.
