import {readFile, readdir} from 'node:fs/promises';
import {join} from 'node:path';
import {deflateRawSync} from 'node:zlib';
const table = Array.from({length:256},(_,n)=>{for(let k=0;k<8;k++)n=(n&1)?0xedb88320^(n>>>1):n>>>1;return n>>>0;});
function crc32(data){let c=0xffffffff;for(const b of data)c=table[(c^b)&255]^(c>>>8);return (c^0xffffffff)>>>0;}
export async function zipDirectory(root, prefix='') {
  const files=[];
  async function walk(dir,relative=''){for(const item of (await readdir(dir,{withFileTypes:true})).sort((a,b)=>a.name.localeCompare(b.name))){const name=relative+item.name;if(item.isDirectory())await walk(join(dir,item.name),name+'/');else if(item.isFile())files.push({name:prefix+name,data:await readFile(join(dir,item.name))});}}
  await walk(root);return zipEntries(files);
}
export function zipEntries(files){
  const chunks=[],directory=[];let offset=0;
  for(const {name,data} of files){
    if(name.startsWith('/')||name.split('/').includes('..'))throw Error('unsafe ZIP path');
    const filename=Buffer.from(name),compressed=deflateRawSync(data,{level:9}),crc=crc32(data);
    const h=Buffer.alloc(30);h.writeUInt32LE(0x04034b50);h.writeUInt16LE(20,4);h.writeUInt16LE(0x800,6);h.writeUInt16LE(8,8);h.writeUInt16LE(33,12);h.writeUInt32LE(crc,14);h.writeUInt32LE(compressed.length,18);h.writeUInt32LE(data.length,22);h.writeUInt16LE(filename.length,26);
    const c=Buffer.alloc(46);c.writeUInt32LE(0x02014b50);c.writeUInt16LE(20,4);c.writeUInt16LE(20,6);c.writeUInt16LE(0x800,8);c.writeUInt16LE(8,10);c.writeUInt16LE(33,14);c.writeUInt32LE(crc,16);c.writeUInt32LE(compressed.length,20);c.writeUInt32LE(data.length,24);c.writeUInt16LE(filename.length,28);c.writeUInt32LE(offset,42);
    chunks.push(h,filename,compressed);directory.push(c,filename);offset+=h.length+filename.length+compressed.length;
  }
  const central=Buffer.concat(directory),end=Buffer.alloc(22);end.writeUInt32LE(0x06054b50);end.writeUInt16LE(files.length,8);end.writeUInt16LE(files.length,10);end.writeUInt32LE(central.length,12);end.writeUInt32LE(offset,16);
  return Buffer.concat([...chunks,central,end]);
}
