import {execFileSync} from 'node:child_process';
import {mkdir, readFile, writeFile, copyFile, readdir, cp} from 'node:fs/promises';
import {join} from 'node:path';
import {createHash} from 'node:crypto';
import {zipDirectory,zipEntries} from './zip.mjs';
const go=process.env.GO_EXE || 'go';
execFileSync(process.execPath,['scripts/build.mjs'],{stdio:'inherit'});
await mkdir('dist/runners',{recursive:true});await mkdir('release',{recursive:true});
const runners={};
for(const arch of ['amd64','arm64']){
  const file='runner-linux-'+arch;
  execFileSync(go,['build','-trimpath','-ldflags=-s -w','-o','../dist/runners/'+file,'.'],{cwd:'runner',stdio:'inherit',env:{...process.env,GOOS:'linux',GOARCH:arch,CGO_ENABLED:'0'}});
  const data=await readFile('dist/runners/'+file);runners[arch]={file,sha256:createHash('sha256').update(data).digest('hex')};
}
await writeFile('dist/runners/manifest.json',JSON.stringify(runners,null,2)+'\n');
const version=JSON.parse(await readFile('package.json','utf8')).version;
for(const file of ['README.md','LICENSE','CHANGELOG.md'])await copyFile(file,'dist/'+file);
await cp('docs','dist/docs',{recursive:true});
const zipName=`komari-webdav-backup-${version}.zip`;
await writeFile('release/'+zipName,await zipDirectory('dist'));
const source=[];
async function collect(dir,relative=''){for(const item of await readdir(dir,{withFileTypes:true})){if(['node_modules','dist','.build','release','.git'].includes(item.name))continue;const name=relative+item.name;if(item.isDirectory())await collect(join(dir,item.name),name+'/');else if(item.isFile())source.push({name:`komari-webdav-backup/${name}`,data:await readFile(join(dir,item.name))});}}
await collect('.');
const sourceName=`komari-webdav-backup-${version}-source.zip`;
await writeFile('release/'+sourceName,zipEntries(source));
const sums=[];for(const name of [zipName,sourceName])sums.push(createHash('sha256').update(await readFile('release/'+name)).digest('hex')+'  '+name);
await writeFile('release/SHA256SUMS',sums.join('\n')+'\n');
console.log('Created '+zipName+' and '+sourceName);
