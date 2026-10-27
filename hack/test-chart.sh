#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export HELM="${HELM:-helm}"
for values in example monitoring; do
  "$HELM" lint --strict charts/herzi -f "hack/chart/$values-values.yaml"
done
python3 - <<'PY'
import copy,json,os,subprocess,tempfile
from pathlib import Path
import yaml
chart=Path('charts/herzi')
helm=os.environ['HELM']
base=yaml.safe_load((chart/'values.yaml').read_text())
fixtures=Path('hack/chart')
example=yaml.safe_load((fixtures/'example-values.yaml').read_text())
namespace='chart-test'
def run(args):
    return subprocess.run([helm,*args],text=True,capture_output=True)
def render(file='example',extra=None,sets=()):
    args=['template','herzi',str(chart),'-n',namespace,'-f',str(fixtures/f'{file}-values.yaml')]
    if extra is not None:
        with tempfile.NamedTemporaryFile(mode='w',suffix='.yaml') as f:
            yaml.safe_dump(extra,f); f.flush()
            result=run([*args,'-f',f.name,*sets])
    else: result=run([*args,*sets])
    assert result.returncode==0,result.stderr
    return [d for d in yaml.safe_load_all(result.stdout) if d]
def get(docs,kind,suffix=''):
    return next(d for d in docs if d['kind']==kind and d['metadata']['name'].endswith(suffix))
def pod(docs):return get(docs,'Deployment')['spec']['template']
def checksum(docs):return pod(docs)['metadata']['annotations']['checksum/startup-config']
def merged(a,b):
    result=copy.deepcopy(a)
    for k,v in b.items(): result[k]=merged(result.get(k,{}),v) if isinstance(v,dict) else v
    return result
rules=yaml.safe_load((chart/'files/rbac-rules.yaml').read_text())
for mode in ['example','monitoring']:
    docs=render(mode)
    actual=get(docs,'ClusterRole','-manager')['rules']
    assert actual==rules,'manager RBAC differs from generated rules'
    for rule in actual:
        assert not set(rule['verbs']) & {'delete','deletecollection','update','create'},rule
        assert '*' not in json.dumps(rule),'wildcard manager RBAC'
    template=pod(docs);spec=template['spec'];container=spec['containers'][0]
    sc=spec['securityContext'];csc=container['securityContext']
    assert sc['runAsNonRoot'] is True and 'runAsUser' not in sc and 'runAsGroup' not in sc
    assert sc['seccompProfile']['type']=='RuntimeDefault'
    assert csc['readOnlyRootFilesystem'] is True and csc['allowPrivilegeEscalation'] is False
    assert csc['capabilities']['drop']==['ALL']
    strategy=get(docs,'Deployment')['spec']['strategy']
    assert strategy=={'type':'RollingUpdate','rollingUpdate':{'maxSurge':0,'maxUnavailable':1}}
    version=yaml.safe_load((chart/'Chart.yaml').read_text())['appVersion']
    assert container['image']==example['image']['repository']+':'+version
    mount=next(m for m in container['volumeMounts'] if m['name']=='config')
    assert mount=={'name':'config','mountPath':'/etc/herzi','readOnly':True}
    cm=get(docs,'ConfigMap','-config');cfg=yaml.safe_load(cm['data']['config.yaml'])
    supplied=yaml.safe_load((fixtures/f'{mode}-values.yaml').read_text())
    expected=merged(base,supplied)['config']
    assert cfg==expected and cfg['defaults']==expected['defaults'] and cfg['pools']==expected['pools']
    for obj in docs:
        labels=obj['metadata']['labels']
        assert labels['app.kubernetes.io/name']=='herzi'
        assert labels['app.kubernetes.io/instance']=='herzi'
        assert labels['app.kubernetes.io/version']==version
        assert labels['app.kubernetes.io/managed-by']=='Helm'
        assert labels['helm.sh/chart']=='herzi-0.1.0'
        if obj['kind'] not in ['ClusterRole','ClusterRoleBinding']:
            assert obj['metadata']['namespace']==namespace,obj
    assert get(docs,'Deployment')['spec']['replicas']==1,'Deployment replicas must equal 1'
    assert spec['terminationGracePeriodSeconds']==30
    assert 'runAsUser' not in csc and 'runAsGroup' not in csc
    assert spec['volumes'][0]=={'name':'config','configMap':{'name':'herzi-config'}}
    env={e['name']:e for e in container['env']};volumes={v['name']:v for v in spec['volumes']}
    assert env['GOMEMLIMIT']['value']=='400MiB'
    if mode=='example':
        assert 'HERZI_TEMPORAL_TLS_CA_FILE' not in env and 'temporal-tls' not in volumes
        assert 'HERZI_API_BEARER_TOKEN' not in env
    else:
        assert env['HERZI_TEMPORAL_TLS_CA_FILE']['value']=='/etc/herzi-temporal/ca.crt'
        assert volumes['temporal-tls']=={'name':'temporal-tls','secret':{'secretName':'herzi-temporal-tls','defaultMode':0o444}}
        assert next(m for m in container['volumeMounts'] if m['name']=='temporal-tls')=={'name':'temporal-tls','mountPath':'/etc/herzi-temporal','readOnly':True}
        assert container['args'][-len(supplied['extraArgs']):]==supplied['extraArgs']
        assert container['env'][-len(supplied['extraEnv']):]==supplied['extraEnv']
        assert spec['volumes'][-len(supplied['extraVolumes']):]==supplied['extraVolumes']
        assert container['volumeMounts'][-len(supplied['extraVolumeMounts']):]==supplied['extraVolumeMounts']
    for kind in ['ServiceMonitor','PrometheusRule']:
        assert any(d['kind']==kind for d in docs)==(mode=='monitoring'),(mode,kind)
    reader_bindings=[d for d in docs if d['kind']=='ClusterRoleBinding' and d['metadata']['name'].endswith('-metrics-reader')]
    assert bool(reader_bindings)==(mode=='monitoring')
    assert get(docs,'Service')['spec']['ports']==[{'name':'https','port':8443,'targetPort':'https','protocol':'TCP'}]
    leader=get(docs,'Role')['rules']
    assert leader==[{'apiGroups':['coordination.k8s.io'],'resources':['leases'],'verbs':['get','list','watch','create','update','patch','delete']},{'apiGroups':[''],'resources':['events'],'verbs':['create','patch']}]
    assert get(docs,'ClusterRole','-metrics-reader')['rules']==[{'nonResourceURLs':['/metrics'],'verbs':['get']}]
    assert get(docs,'ClusterRole','-metrics-auth')['rules']==[{'apiGroups':['authentication.k8s.io'],'resources':['tokenreviews'],'verbs':['create']},{'apiGroups':['authorization.k8s.io'],'resources':['subjectaccessreviews'],'verbs':['create']}]
    for obj in docs:
        if obj['kind'].endswith('Binding'):
            assert obj['subjects']==(supplied['rbac']['metricsReader']['subjects'] if obj['metadata']['name'].endswith('-metrics-reader') else [{'kind':'ServiceAccount','name':'herzi','namespace':namespace}])
            assert obj['roleRef']=={'apiGroup':'rbac.authorization.k8s.io','kind':'Role' if obj['kind']=='RoleBinding' else 'ClusterRole','name':obj['metadata']['name']}
    if mode=='monitoring':
        assert '{{ $labels.pool }}' in yaml.safe_dump(get(docs,'PrometheusRule')['spec'])
        assert get(docs,'PrometheusRule')['spec']==yaml.safe_load((chart/'files/alerts.rules.yaml').read_text())
        assert reader_bindings[0]['subjects']==supplied['rbac']['metricsReader']['subjects']
        monitor=get(docs,'ServiceMonitor')['spec']
        assert monitor['selector']['matchLabels']=={'app.kubernetes.io/name':'herzi','app.kubernetes.io/instance':'herzi'}
        assert monitor['endpoints']==[dict(path='/metrics',port='https',scheme='https',interval='30s',**supplied['serviceMonitor']['endpoint'])]
        for kind in ['ServiceMonitor','PrometheusRule']:assert get(docs,kind)['metadata']['labels']['release']=='prometheus'
original=checksum(render())
for extra in [{'config':{'pools':{'32-128-zone-1':{'minFree':6}}}},{'config':{'interval':'3m','reconcileTimeout':'50s','ambiguousWait':'6m','defaults':{'highPct':20}}}]:
    assert checksum(render(extra=extra))==original,'live config caused a restart'
for key,value in {'hubName':'ocp4-other','agentNamespace':'other','installURL':'https://other.invalid/install','stuckAfter':'3h','temporalAddress':'other:7233','temporalNamespace':'other','activityQueue':'other-a','workflowQueue':'other-w','segmentQueue':'other-s'}.items():
    assert checksum(render(extra={'config':{key:value}}))!=original,key+' did not cause a restart'
import hashlib
startup={k:merged(base,example)['config'][k] for k in ['hubName','agentNamespace','installURL','maxSlots','stuckAfter','temporalAddress','temporalNamespace','activityQueue','workflowQueue','segmentQueue']}
assert original==hashlib.sha256(json.dumps(startup,sort_keys=True,separators=(',',':')).encode()).hexdigest()
docs=render(extra={'rbac':{'create':False},'serviceAccount':{'create':False,'name':'existing'},'metrics':{'enabled':False}})
assert not any(d['kind'] in ['Role','RoleBinding','ClusterRole','ClusterRoleBinding','ServiceAccount','Service'] for d in docs)
assert pod(docs)['spec']['serviceAccountName']=='existing'
result=run(['template','herzi',str(chart)])
assert result.returncode!=0
for key in ['hubName','installURL','temporalAddress','repository']:assert key in result.stderr,result.stderr
def invalid(extra,words):
    with tempfile.NamedTemporaryFile(mode='w',suffix='.yaml') as f:
        yaml.safe_dump(extra,f);f.flush()
        for command in ['template','lint']:
            args=[command,'herzi',str(chart)] if command=='template' else [command,'--strict',str(chart)]
            result=run([*args,'-f',str(fixtures/'example-values.yaml'),'-f',f.name])
            assert result.returncode!=0,(command,extra)
            for word in words:assert word in result.stderr+result.stdout,(extra,result.stderr,result.stdout)
for key in ['lowPct','minBand']:
    for value in [0,None]:
        for location in [{'defaults':{key:value}},{'pools':{'example':{key:value}}}]:invalid({'config':location},[key,'removed'])
invalid({'config':{'maxSlots':50}},['maxSlots','100'])
invalid({'config':{'defaults':{'highPct':1001}}},['highPct'])
invalid({'config':{'pools':{'example':{'minFree':100001}}}},['minFree'])
invalid({'config':{'defaults':{'typo':1}}},['typo'])
invalid({'config':{'interval':'20minutes'}},['interval'])
invalid({'config':{'defaults':{'paused':'true'}}},['paused'])
for key,value in {'networkPolicy':{'enabled':True},'replicaCount':2,'nameOverride':'old','fullnameOverride':'old','topologySpreadConstraints':[],'priorityClassName':'old','installApi':{'bearerToken':{'existingSecret':'old'}}}.items():
    invalid({key:value},[key,'not allowed'])
for key,value in [('mtls',{'enabled':True}),('apiKey',{})]:invalid({'temporal':{key:value}},[key,'not allowed'])
invalid({'metrics':{'tls':{}}},['tls','not allowed'])
for key in ['scrapeTimeout','tlsConfig','bearerTokenSecret']:invalid({'serviceMonitor':{key:{}}},[key,'not allowed'])
invalid({'typo':True},['typo','not allowed'])
for key in ['image','serviceAccount','rbac','temporal','metrics','serviceMonitor','prometheusRule']:
    invalid({key:{'typo':True}},['typo','not allowed'])
docs=render(sets=['--set-string','image.tag=custom'])
assert pod(docs)['spec']['containers'][0]['image']==example['image']['repository']+':custom'
docs=render(extra={'temporal':{'tls':{'existingSecret':'custom-ca','caKey':'root.pem'}}})
assert next(e for e in pod(docs)['spec']['containers'][0]['env'] if e['name']=='HERZI_TEMPORAL_TLS_CA_FILE')['value']=='/etc/herzi-temporal/root.pem'
assert next(v for v in pod(docs)['spec']['volumes'] if v['name']=='temporal-tls')['secret']['secretName']=='custom-ca'
for kind,key in [('ServiceMonitor','serviceMonitor'),('PrometheusRule','prometheusRule')]:
    docs=render(extra={key:{'enabled':True}})
    assert any(d['kind']==kind for d in docs)
    other='PrometheusRule' if kind=='ServiceMonitor' else 'ServiceMonitor'
    assert not any(d['kind']==other for d in docs)
for key,kinds in [('rbac',['Role','RoleBinding','ClusterRole','ClusterRoleBinding']),('serviceAccount',['ServiceAccount']),('metrics',['Service'])]:
    docs=render(extra={key:{'create' if key!='metrics' else 'enabled':False}})
    assert not any(d['kind'] in kinds for d in docs),(key,kinds)
    assert any(d['kind']=='Deployment' for d in docs)
monitor=get(render(extra={'serviceMonitor':{'enabled':True,'interval':'1m','endpoint':{'path':'/custom','scheme':'http','port':'custom'}}}),'ServiceMonitor')['spec']['endpoints'][0]
assert monitor=={'path':'/custom','scheme':'http','port':'custom','interval':'1m'}
assert pod(render(extra={'serviceAccount':{'create':False}}))['spec']['serviceAccountName']=='default'
extra={'serviceAccount':{'name':'chosen','annotations':{'example':'yes'}},'podLabels':{'custom':'yes','app.kubernetes.io/name':'wrong'},'podAnnotations':{'custom':'yes','checksum/startup-config':'wrong'},'nodeSelector':{'zone':'one'},'tolerations':[{'key':'dedicated','operator':'Exists'}],'affinity':{'nodeAffinity':{'preferredDuringSchedulingIgnoredDuringExecution':[]}},'imagePullSecrets':[{'name':'registry'}],'resources':{'limits':{'cpu':'2'}},'goMemLimit':'500MiB'}
docs=render(extra=extra);spec=pod(docs)['spec'];container=spec['containers'][0]
assert get(docs,'ServiceAccount')['metadata']['name']=='chosen'
assert get(docs,'ServiceAccount')['metadata']['annotations']=={'example':'yes'}
assert spec['serviceAccountName']=='chosen'
for key in ['nodeSelector','tolerations','affinity','imagePullSecrets']:assert spec[key]==extra[key]
assert container['resources']==merged(base,extra)['resources']
assert container['env'][0]=={'name':'GOMEMLIMIT','value':'500MiB'}
assert pod(docs)['metadata']['labels']['custom']=='yes' and pod(docs)['metadata']['labels']['app.kubernetes.io/name']=='herzi'
assert pod(docs)['metadata']['annotations']['custom']=='yes' and checksum(docs)==original
result=run(['template','other-release',str(chart),'-n',namespace,'-f',str(fixtures/'example-values.yaml')])
assert result.returncode==0,result.stderr
for obj in yaml.safe_load_all(result.stdout):
    if obj:
        assert obj['metadata']['name'].startswith('other-release')
        assert obj['metadata']['labels']['app.kubernetes.io/instance']=='other-release'
with tempfile.TemporaryDirectory() as directory:
    result=run(['package',str(chart),'--destination',directory])
    assert result.returncode==0,result.stderr
    archive=next(Path(directory).glob('*.tgz'))
    result=subprocess.run(['tar','-tzf',str(archive)],text=True,capture_output=True,check=True)
    expected=['Chart.yaml','values.yaml','values.schema.json','.helmignore']
    expected += ['templates/'+name for name in ['NOTES.txt','_helpers.tpl','serviceaccount.yaml','clusterrole.yaml','clusterrolebinding.yaml','role.yaml','rolebinding.yaml','metrics-auth.yaml','configmap.yaml','deployment.yaml','service.yaml','servicemonitor.yaml','prometheusrule.yaml']]
    expected += ['files/rbac-rules.yaml','files/alerts.rules.yaml']
    assert sorted(result.stdout.splitlines())==sorted('herzi/'+name for name in expected),result.stdout
    print('Package allowlist passed: '+str(len(expected))+' files')
print('Chart assertions passed: RBAC, hardening, replicas, strategy, images, config, labels, namespaces, names, checksum, CA, generic knobs, monitoring, toggles, scheduling, schema, package')
PY
